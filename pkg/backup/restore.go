// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cockroachdb/cockroach/pkg/config/zonepb"
	"github.com/cockroachdb/cockroach/pkg/jobs"
	"github.com/cockroachdb/cockroach/pkg/jobs/jobspb"
	"github.com/cockroachdb/cockroach/pkg/keys"
	"github.com/cockroachdb/cockroach/pkg/kv"
	"github.com/cockroachdb/cockroach/pkg/kv/kvserver/kvserverbase"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/security/username"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/bootstrap"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/catalogkeys"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/dbdesc"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descbuilder"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descs"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/nstree"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/tabledesc"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/typedesc"
	"github.com/cockroachdb/cockroach/pkg/sql/schemachanger/scbackup"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/catconstants"
	"github.com/cockroachdb/cockroach/pkg/storage"
	"github.com/cockroachdb/cockroach/pkg/util/encoding"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/planted"
	"github.com/cockroachdb/errors"
)

// A cluster restore works like upstream's full cluster restore: into a
// cluster with no user databases or tables, it brings back every
// database, schema, table, type, function and view with its ID, the data
// of the tables as of the target time, the users, roles and grants, the
// cluster settings, zone configs, comments and table statistics. The
// pre-loaded defaultdb and postgres are replaced by those of the backup.
// Jobs, schedules and the repository settings are not restored; the
// restored cluster backs up wherever its own --config says.
//
// The job loads the full backup and the segments into a Pebble engine in
// a temporary folder and reads it as of the target time. It writes the
// table data with AddSSTable at a new timestamp, then publishes the
// catalog and the system tables in one transaction.
//
// Objects that were being dropped at the target time are left out, since
// they were not visible then, and so are CREATE TABLE AS tables and
// materialized views that were being created; references to them go. A
// table that was taken offline by an IMPORT INTO comes back online with the
// data it had before the import; a table that the import itself created is
// left out. Schema changes that were running at the target time,
// including tables being created, get new jobs and carry on; a table being
// created comes back without rows, which its job writes.

// restoredSystemTables are the system tables whose rows a cluster restore
// brings back, by name. descriptor and namespace are handled on their own.
// descriptor_id_seq is not copied: the claim raised it to at least the
// backup's value.
var restoredSystemTables = []catconstants.SystemTableName{
	catconstants.UsersTableName,
	catconstants.RoleMembersTableName,
	catconstants.RoleOptionsTableName,
	catconstants.DatabaseRoleSettingsTableName,
	catconstants.SystemPrivilegeTableName,
	catconstants.RoleIDSequenceName,
	catconstants.SettingsTableName,
	catconstants.ZonesTableName,
	catconstants.CommentsTableName,
	catconstants.TableStatisticsTableName,
	catconstants.LocationsTableName,
	catconstants.UITableName,
	catconstants.SystemExternalConnectionsTableName,
}

// cacheTables are the system tables whose descriptor version the restore
// bumps, so that the caches of users, roles and privileges are reloaded.
var cacheTables = []catconstants.SystemTableName{
	catconstants.UsersTableName,
	catconstants.RoleMembersTableName,
	catconstants.RoleOptionsTableName,
	catconstants.DatabaseRoleSettingsTableName,
	catconstants.SystemPrivilegeTableName,
}

type restoreResumer struct {
	job *jobs.Job
}

var _ jobs.Resumer = (*restoreResumer)(nil)

// Resume implements jobs.Resumer.
func (r *restoreResumer) Resume(ctx context.Context, execCtx interface{}) error {
	if r.job.FractionCompleted() >= 1 {
		// The catalog was published before the job could finish.
		return nil
	}
	execCfg := execCtx.(sql.JobExecContext).ExecCfg()
	d := r.job.Details().(jobspb.ClusterRestoreDetails)
	store, err := OpenStore(ctx, Repository{Bucket: d.Bucket, Prefix: d.Prefix, Endpoint: d.Endpoint, Region: d.Region})
	if err != nil {
		return err
	}
	c, err := findChain(ctx, store, d.Target)
	if err != nil {
		return err
	}
	if d.Target.IsEmpty() {
		// Fix "latest" before writing anything, so that a retry restores to
		// the same time.
		d.Target = c.end()
		if err := r.job.NoTxn().SetDetails(ctx, d); err != nil {
			return err
		}
	}
	target := d.Target
	if v := execCfg.Settings.Version.ActiveVersion(ctx).String(); v != c.full.ClusterVersion {
		return errors.Newf("the backup has cluster version %s and this cluster %s; they must be the same",
			c.full.ClusterVersion, v)
	}
	// Fail early; claimCluster checks again before any row is written.
	if err := claimCluster(ctx, execCfg, r.job, 0); err != nil {
		return err
	}
	log.Infof(ctx, "restoring to %s from full backup %s and %d segments",
		FormatTime(target), c.full.Name, len(c.segments))

	dir, err := os.MkdirTemp(execCfg.DistSQLSrv.ServerConfig.TempStoragePath, "backup-restore-")
	if err != nil {
		return err
	}
	defer removeAll(ctx, dir)
	eng, err := load(ctx, execCfg.Settings, store, c, dir)
	if err != nil {
		return err
	}
	defer eng.Close()

	src, err := readCatalog(ctx, eng, target)
	if err != nil {
		return err
	}
	backupNext, err := src.nextDescID(ctx, eng)
	if err != nil {
		return err
	}
	if err := claimCluster(ctx, execCfg, r.job, backupNext); err != nil {
		return err
	}
	if err := ingestTables(ctx, execCfg, eng, src); err != nil {
		return err
	}
	return publish(ctx, execCfg, eng, src, r.job)
}

// OnFailOrCancel implements jobs.Resumer. The rows written so far have no
// descriptors. No new object gets their IDs, since the claim raised the
// descriptor ID sequence past them first, and no other restore can claim
// this cluster; restore into a new cluster instead.
func (r *restoreResumer) OnFailOrCancel(context.Context, interface{}, error) error { return nil }

// CollectProfile implements jobs.Resumer.
func (r *restoreResumer) CollectProfile(context.Context, interface{}) error { return nil }

// claimCluster makes sure that this restore is the only thing that
// writes into this cluster's catalog. The claim is the descriptor ID
// sequence: a cluster can be claimed only while the sequence is where a new
// cluster has it, so no table was ever created in it and no other restore
// claimed it, and claiming it raises the sequence past that value and to
// at least the backup's next ID, raiseTo. The check and the raise are one
// transaction, so of a concurrent CREATE TABLE or restore one fails. The
// job marks itself as the owner in the same transaction (a fraction
// completed above 0) and does not check again when it resumes. With
// raiseTo == 0 claimCluster only checks.
func claimCluster(
	ctx context.Context, execCfg *sql.ExecutorConfig, job *jobs.Job, raiseTo int64,
) error {
	if job.FractionCompleted() >= claimedFraction {
		return nil
	}
	return execCfg.InternalDB.DescsTxn(ctx, func(ctx context.Context, txn descs.Txn) error {
		all, err := txn.Descriptors().GetAllDescriptors(ctx, txn.KV())
		if err != nil {
			return err
		}
		if err := onlyPreloaded(all); err != nil {
			return err
		}
		key := codec.SequenceKey(keys.DescIDSequenceID)
		res, err := txn.KV().Get(ctx, key)
		if err != nil {
			return err
		}
		next, err := res.Value.GetInt()
		if err != nil {
			return err
		}
		if next != pristineNextID() {
			return errors.New("this cluster has handed out descriptor IDs: a table was created " +
				"in it or another restore claimed it; restore into a new cluster")
		}
		if raiseTo == 0 {
			return nil
		}
		if err := job.WithTxn(txn).FractionProgressed(ctx, jobs.FractionUpdater(claimedFraction)); err != nil {
			return err
		}
		if raiseTo <= next {
			raiseTo = next + 1
		}
		return txn.KV().Put(ctx, key, raiseTo)
	})
}

// pristineNextID is the descriptor ID sequence of a new cluster: the first
// ID after the system tables, defaultdb and postgres and their public
// schemas.
func pristineNextID() int64 {
	ms := bootstrap.MakeMetadataSchema(codec, zonepb.DefaultZoneConfigRef(), zonepb.DefaultSystemZoneConfigRef())
	return int64(ms.FirstNonSystemDescriptorID()) + 2*int64(len(catalogkeys.DefaultUserDBs))
}

// onlyPreloaded returns an error if all has user objects other than the
// pre-loaded defaultdb and postgres.
func onlyPreloaded(all nstree.Catalog) error {
	preloaded := map[descpb.ID]bool{}
	return all.ForEachDescriptor(func(desc catalog.Descriptor) error {
		if catalog.IsSystemDescriptor(desc) {
			return nil
		}
		switch {
		case desc.DescriptorType() == catalog.Database &&
			(desc.GetName() == catalogkeys.DefaultDatabaseName || desc.GetName() == catalogkeys.PgDatabaseName):
			preloaded[desc.GetID()] = true
			return nil
		case desc.DescriptorType() == catalog.Schema && desc.GetName() == catconstants.PublicSchemaName &&
			preloaded[desc.GetParentID()]:
			return nil
		}
		return errors.Newf("the cluster is not empty: it has %s %q; restore into a new cluster",
			desc.DescriptorType(), desc.GetName())
	})
}

// claimedFraction is the fraction completed of a restore that has claimed
// its cluster.
const claimedFraction = 0.01

// nextDescID returns the value of the backup's descriptor ID sequence at
// the target: the first ID that no object of the backup has.
func (src *sourceCatalog) nextDescID(ctx context.Context, eng storage.Engine) (int64, error) {
	srcID, ok := src.systemIDs[string(catconstants.DescIDSequenceTableName)]
	if !ok {
		return 0, errors.New("the backup has no descriptor ID sequence")
	}
	res, err := storage.MVCCGet(ctx, eng, codec.SequenceKey(uint32(srcID)), src.target, storage.MVCCGetOptions{})
	if err != nil {
		return 0, err
	}
	if res.Value == nil {
		return 0, errors.New("the backup's descriptor ID sequence has no value")
	}
	return res.Value.GetInt()
}

// removeAll removes a temporary folder and logs a failure.
func removeAll(ctx context.Context, dir string) {
	if err := os.RemoveAll(dir); err != nil {
		log.Warningf(ctx, "removing %s: %v", dir, err)
	}
}

// load opens a Pebble engine in dir and ingests the files of chain c.
func load(
	ctx context.Context, st *cluster.Settings, s *Store, c chain, dir string,
) (storage.Engine, error) {
	eng, err := storage.Open(ctx, storage.Filesystem(filepath.Join(dir, "engine")), st,
		storage.CacheSize(64<<20))
	if err != nil {
		return nil, err
	}
	n := 0
	write := func(sst []byte) (string, error) {
		n++
		p := filepath.Join(dir, fmt.Sprintf("%08d.sst", n))
		return p, os.WriteFile(p, sst, 0600)
	}
	var paths []string
	for _, f := range c.full.Files {
		sst, err := readFullFile(ctx, s, c.full, f)
		if err != nil {
			eng.Close()
			return nil, err
		}
		p, err := write(sst)
		if err != nil {
			eng.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	if err := ingest(ctx, eng, paths); err != nil {
		eng.Close()
		return nil, err
	}
	segments := c.segments
	if planted.On(planted.LostSegment) && len(segments) > 0 {
		segments = segments[:len(segments)-1]
	}
	for _, h := range segments {
		paths = paths[:0]
		if _, err := readSegment(ctx, s, h.Number, false, func(_ FileInfo, sst []byte) error {
			p, err := write(sst)
			paths = append(paths, p)
			return err
		}); err != nil {
			eng.Close()
			return nil, err
		}
		if err := ingest(ctx, eng, paths); err != nil {
			eng.Close()
			return nil, err
		}
	}
	return eng, nil
}

// ingest adds files that do not overlap each other to eng.
func ingest(ctx context.Context, eng storage.Engine, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return errors.Wrap(eng.IngestLocalFiles(ctx, paths), "loading backup files")
}

// sourceCatalog is the catalog of the backup at the target time, and what
// the restore does with it.
type sourceCatalog struct {
	target hlc.Timestamp
	// restored are the descriptors to restore, changed as the restore needs.
	restored map[descpb.ID]catalog.MutableDescriptor
	// skipped are the user descriptors at the target time that are not
	// restored.
	skipped map[descpb.ID]bool
	// systemIDs maps the system tables by name to their IDs in the backup.
	systemIDs map[string]descpb.ID
	// readAt is the time to read a table's data at: before its import for
	// a table that an IMPORT INTO had taken offline, else the target.
	readAt map[descpb.ID]hlc.Timestamp
	// noRows are the tables restored without rows: those being created.
	noRows map[descpb.ID]bool
}

var codec = keys.SystemSQLCodec

func tableSpan(id uint32) roachpb.Span {
	p := codec.TablePrefix(id)
	return roachpb.Span{Key: p, EndKey: p.PrefixEnd()}
}

// readCatalog reads the namespace and the descriptors at target and
// decides what to restore.
func readCatalog(
	ctx context.Context, eng storage.Engine, target hlc.Timestamp,
) (*sourceCatalog, error) {
	src := &sourceCatalog{
		target:    target,
		restored:  map[descpb.ID]catalog.MutableDescriptor{},
		skipped:   map[descpb.ID]bool{},
		systemIDs: map[string]descpb.ID{},
		readAt:    map[descpb.ID]hlc.Timestamp{},
		noRows:    map[descpb.ID]bool{},
	}
	span := tableSpan(keys.NamespaceTableID)
	if _, err := storage.MVCCIterate(ctx, eng, span.Key, span.EndKey, target, storage.MVCCScanOptions{},
		func(kv roachpb.KeyValue) error {
			name, err := catalogkeys.DecodeNameMetadataKey(codec, kv.Key)
			if err != nil {
				return err
			}
			id, err := kv.Value.GetInt()
			if err != nil {
				return err
			}
			if name.ParentID == keys.SystemDatabaseID && name.ParentSchemaID == keys.SystemPublicSchemaID {
				src.systemIDs[name.Name] = descpb.ID(id)
			}
			return nil
		}); err != nil {
		return nil, errors.Wrap(err, "reading the namespace of the backup")
	}
	span = tableSpan(keys.DescriptorTableID)
	if _, err := storage.MVCCIterate(ctx, eng, span.Key, span.EndKey, target, storage.MVCCScanOptions{},
		func(kv roachpb.KeyValue) error {
			b, err := descbuilder.FromSerializedValue(&kv.Value)
			if err != nil || b == nil {
				return err
			}
			desc := b.BuildCreatedMutable()
			if catalog.IsSystemDescriptor(desc) {
				return nil
			}
			if !src.restore(desc) {
				src.skipped[desc.GetID()] = true
				return nil
			}
			src.restored[desc.GetID()] = desc
			return nil
		}); err != nil {
		return nil, errors.Wrap(err, "reading the descriptors of the backup")
	}
	for _, desc := range src.restored {
		src.dropReferences(desc)
	}
	return src, nil
}

// dropReferences removes from desc the references to objects that are not
// restored.
func (src *sourceCatalog) dropReferences(desc catalog.MutableDescriptor) {
	switch d := desc.(type) {
	case *tabledesc.Mutable:
		refs := d.DependedOnBy[:0]
		for _, r := range d.DependedOnBy {
			if !src.skipped[r.ID] {
				refs = append(refs, r)
			}
		}
		d.DependedOnBy = refs
		fks := d.InboundFKs[:0]
		for _, fk := range d.InboundFKs {
			if !src.skipped[fk.OriginTableID] {
				fks = append(fks, fk)
			}
		}
		d.InboundFKs = fks
	case *typedesc.Mutable:
		ids := d.ReferencingDescriptorIDs[:0]
		for _, id := range d.ReferencingDescriptorIDs {
			if !src.skipped[id] {
				ids = append(ids, id)
			}
		}
		d.ReferencingDescriptorIDs = ids
	case *dbdesc.Mutable:
		for name, sc := range d.Schemas {
			if src.skipped[sc.ID] {
				delete(d.Schemas, name)
			}
		}
	}
}

// restore decides whether desc is restored and changes it as needed.
func (src *sourceCatalog) restore(desc catalog.MutableDescriptor) bool {
	switch {
	case desc.Dropped():
		return false
	case desc.Adding():
		if tbl, ok := desc.(*tabledesc.Mutable); ok && (tbl.IsAs() || tbl.MaterializedView()) {
			// A CREATE TABLE AS or materialized view that had not returned:
			// its job would run its query at a time of the source cluster.
			return false
		}
		// Its schema change job finishes it; it has no rows yet.
		src.noRows[desc.GetID()] = true
	case desc.Offline():
		tbl, ok := desc.(*tabledesc.Mutable)
		if !ok || tbl.OfflineReason != importingReason {
			return false
		}
		if tbl.ImportStartWallTime == 0 && tbl.Version == 1 {
			// The import created the table: it did not exist before.
			return false
		}
		// IMPORT INTO had taken the table offline: bring it back with the
		// data from before the import. The import writes no rows before it
		// sets ImportStartWallTime.
		if tbl.ImportStartWallTime != 0 {
			src.readAt[tbl.ID] = hlc.Timestamp{WallTime: tbl.ImportStartWallTime - 1}
		}
		tbl.State = descpb.DescriptorState_PUBLIC
		tbl.OfflineReason = ""
		tbl.ImportStartWallTime = 0
	}
	return true
}

// importingReason is the offline reason of a table that IMPORT writes into.
const importingReason = "importing"

// ingestTables writes the data of the restored tables, as of the target
// time, with AddSSTable at a new timestamp.
func ingestTables(
	ctx context.Context, execCfg *sql.ExecutorConfig, eng storage.Engine, src *sourceCatalog,
) error {
	adder, err := execCfg.DistSQLSrv.ServerConfig.BulkAdder(ctx, execCfg.DB, execCfg.Clock.Now(),
		kvserverbase.BulkAdderOptions{Name: "restore", WriteAtBatchTimestamp: true})
	if err != nil {
		return err
	}
	defer adder.Close(ctx)
	for _, desc := range orderedByID(src.restored) {
		tbl, ok := desc.(*tabledesc.Mutable)
		if !ok || !tbl.IsPhysicalTable() || src.noRows[tbl.ID] {
			continue
		}
		if tbl.GetExcludeDataFromBackup() {
			// The table comes back empty, as upstream's did.
			continue
		}
		readAt := src.target
		if t, ok := src.readAt[tbl.ID]; ok {
			readAt = t
		}
		for _, idx := range tbl.AllIndexes() {
			span := tbl.IndexSpan(codec, idx.GetID())
			if _, err := storage.MVCCIterate(ctx, eng, span.Key, span.EndKey, readAt, storage.MVCCScanOptions{},
				func(kv roachpb.KeyValue) error {
					return adder.Add(ctx, kv.Key, kv.Value.RawBytes)
				}); err != nil {
				return errors.Wrapf(err, "restoring the data of table %q", tbl.Name)
			}
		}
	}
	return adder.Flush(ctx)
}

func orderedIDs(m map[descpb.ID]catalog.MutableDescriptor) descpb.IDs {
	var ids descpb.IDs
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func orderedByID(m map[descpb.ID]catalog.MutableDescriptor) []catalog.MutableDescriptor {
	ids := orderedIDs(m)
	out := make([]catalog.MutableDescriptor, len(ids))
	for i, id := range ids {
		out[i] = m[id]
	}
	return out
}

// publish replaces the catalog and the restored system tables of this
// cluster with those of the backup, in one transaction.
func publish(
	ctx context.Context,
	execCfg *sql.ExecutorConfig,
	eng storage.Engine,
	src *sourceCatalog,
	job *jobs.Job,
) error {
	return execCfg.InternalDB.DescsTxn(ctx, func(ctx context.Context, txn descs.Txn) error {
		col := txn.Descriptors()
		kvTxn := txn.KV()
		targetIDs, err := systemTableIDs(ctx, txn)
		if err != nil {
			return err
		}

		// Take out the pre-loaded databases and their names. Their IDs
		// may come back with the backup's descriptors; those then get a
		// higher version, so that leases on the old ones end.
		all, err := col.GetAllDescriptors(ctx, kvTxn)
		if err != nil {
			return err
		}
		if err := onlyPreloaded(all); err != nil {
			return err
		}
		oldVersions := map[descpb.ID]descpb.DescriptorVersion{}
		b := kvTxn.NewBatch()
		if err := all.ForEachDescriptor(func(desc catalog.Descriptor) error {
			if catalog.IsSystemDescriptor(desc) {
				return nil
			}
			oldVersions[desc.GetID()] = desc.GetVersion()
			b.Del(catalogkeys.MakeDescMetadataKey(codec, desc.GetID()))
			if desc.DescriptorType() != catalog.Schema || desc.GetName() != catconstants.PublicSchemaName {
				b.Del(catalogkeys.EncodeNameKey(codec, desc))
			} else {
				b.Del(catalogkeys.EncodeNameKey(codec, &descpb.NameInfo{
					ParentID: desc.GetParentID(), Name: desc.GetName(),
				}))
			}
			return nil
		}); err != nil {
			return err
		}
		if err := kvTxn.Run(ctx, b); err != nil {
			return err
		}

		// The namespace of the backup, without the system entries and the
		// names of objects that are not restored.
		if err := copyRows(ctx, kvTxn, eng, src.target, keys.NamespaceTableID, keys.NamespaceTableID,
			func(kv roachpb.KeyValue) bool {
				name, err := catalogkeys.DecodeNameMetadataKey(codec, kv.Key)
				if err != nil || name.ParentID == keys.SystemDatabaseID ||
					(name.ParentID == 0 && name.Name == catconstants.SystemDatabaseName) {
					return false
				}
				id, err := kv.Value.GetInt()
				return err == nil && !src.skipped[descpb.ID(id)]
			}, true /* keepExisting */); err != nil {
			return err
		}

		for _, name := range restoredSystemTables {
			srcID, ok := src.systemIDs[string(name)]
			if !ok {
				return errors.Newf("the backup has no system table %s", name)
			}
			keep, err := src.keepRow(name)
			if err != nil {
				return err
			}
			dstID, ok := targetIDs[string(name)]
			if !ok {
				return errors.Newf("this cluster has no system table %s", name)
			}
			if err := copyRows(ctx, kvTxn, eng, src.target, srcID, dstID, keep, false); err != nil {
				return errors.Wrapf(err, "restoring system.%s", name)
			}
		}

		// The descriptors are written as they are, then read back and
		// written again through the collection, which bumps their versions
		// and validates them. A pre-loaded ID that comes back gets a version
		// above its old one, so that leases on the old one end.
		b = kvTxn.NewBatch()
		for _, desc := range orderedByID(src.restored) {
			if v, ok := oldVersions[desc.GetID()]; ok && desc.GetVersion() < v {
				setVersion(desc, v)
			}
			b.Put(catalogkeys.MakeDescMetadataKey(codec, desc.GetID()), desc.DescriptorProto())
		}
		if err := kvTxn.Run(ctx, b); err != nil {
			return err
		}
		// Forget the catalog read before the writes above.
		col.ReleaseAll(ctx)
		mut := nstree.MutableCatalog{}
		var tables []*tabledesc.Mutable
		for _, id := range orderedIDs(src.restored) {
			desc, err := col.MutableByID(kvTxn).Desc(ctx, id)
			if err != nil {
				return err
			}
			mut.UpsertDescriptor(desc)
			if tbl, ok := desc.(*tabledesc.Mutable); ok {
				tables = append(tables, tbl)
			}
		}
		if _, err := scbackup.CreateDeclarativeSchemaChangeJobs(ctx, execCfg.JobRegistry, txn, mut.Catalog); err != nil {
			return err
		}
		if err := legacySchemaChangeJobs(ctx, execCfg, txn, tables); err != nil {
			return err
		}
		if err := ttlSchedules(ctx, execCfg, txn, tables); err != nil {
			return err
		}
		if err := typeSchemaChangeJobs(ctx, execCfg, txn, mut.Catalog); err != nil {
			return err
		}
		b = kvTxn.NewBatch()
		if err := mut.ForEachDescriptor(func(desc catalog.Descriptor) error {
			return col.WriteDescToBatch(ctx, false, desc.(catalog.MutableDescriptor), b)
		}); err != nil {
			return err
		}
		for _, name := range cacheTables {
			tbl, err := col.MutableByID(kvTxn).Table(ctx, targetIDs[string(name)])
			if err != nil {
				return err
			}
			if err := col.WriteDescToBatch(ctx, false, tbl, b); err != nil {
				return err
			}
		}
		if err := kvTxn.Run(ctx, b); err != nil {
			return err
		}
		if err := col.ValidateUncommittedDescriptors(ctx, kvTxn); err != nil {
			return err
		}
		return job.WithTxn(txn).FractionProgressed(ctx, jobs.FractionUpdater(1))
	})
}

func setVersion(desc catalog.MutableDescriptor, v descpb.DescriptorVersion) {
	switch d := desc.DescriptorProto().Union.(type) {
	case *descpb.Descriptor_Table:
		d.Table.Version = v
	case *descpb.Descriptor_Database:
		d.Database.Version = v
	case *descpb.Descriptor_Schema:
		d.Schema.Version = v
	case *descpb.Descriptor_Type:
		d.Type.Version = v
	case *descpb.Descriptor_Function:
		d.Function.Version = v
	}
}

// systemTableIDs returns the IDs of this cluster's system tables by name.
func systemTableIDs(ctx context.Context, txn descs.Txn) (map[string]descpb.ID, error) {
	ids := map[string]descpb.ID{}
	all, err := txn.Descriptors().GetAllDescriptors(ctx, txn.KV())
	if err != nil {
		return nil, err
	}
	err = all.ForEachDescriptor(func(desc catalog.Descriptor) error {
		if desc.GetParentID() == keys.SystemDatabaseID && desc.DescriptorType() == catalog.Table {
			ids[desc.GetName()] = desc.GetID()
		}
		return nil
	})
	return ids, err
}

// keepRow returns which rows of system table name the restore keeps, or
// nil for all.
func (src *sourceCatalog) keepRow(
	name catconstants.SystemTableName,
) (func(roachpb.KeyValue) bool, error) {
	// firstColumn decodes the first primary key column, an INT, of a key.
	firstColumn := func(kv roachpb.KeyValue, skip int) (int64, bool) {
		rest, _, _, err := codec.DecodeIndexPrefix(kv.Key)
		if err != nil {
			return 0, false
		}
		var v int64
		for i := 0; i <= skip; i++ {
			if rest, v, err = encoding.DecodeVarintAscending(rest); err != nil {
				return 0, false
			}
		}
		return v, true
	}
	// object keeps the rows of objects that are restored or not user
	// objects.
	object := func(skip int) func(roachpb.KeyValue) bool {
		return func(kv roachpb.KeyValue) bool {
			id, ok := firstColumn(kv, skip)
			return ok && !src.skipped[descpb.ID(id)]
		}
	}
	switch name {
	case catconstants.SettingsTableName:
		// The cluster version stays this cluster's.
		return func(kv roachpb.KeyValue) bool {
			rest, _, _, err := codec.DecodeIndexPrefix(kv.Key)
			if err != nil {
				return false
			}
			_, setting, err := encoding.DecodeUnsafeStringAscending(rest, nil)
			return err == nil && setting != "version"
		}, nil
	case catconstants.ZonesTableName, catconstants.TableStatisticsTableName:
		return object(0), nil
	case catconstants.CommentsTableName:
		// The key is (type, object_id, sub_id).
		return object(1), nil
	}
	return nil, nil
}

// copyRows replaces the rows of table dstID that keep accepts (all if
// keep is nil) with the rows of table srcID in the backup at target that
// keep accepts. The keys move to the prefix of dstID. With
// keepExisting, no row of dstID is deleted.
func copyRows(
	ctx context.Context,
	txn *kv.Txn,
	eng storage.Engine,
	target hlc.Timestamp,
	srcID, dstID descpb.ID,
	keep func(roachpb.KeyValue) bool,
	keepExisting bool,
) error {
	if keep == nil {
		keep = func(roachpb.KeyValue) bool { return true }
	}
	// flushBytes is about how much a batch writes before it is sent.
	const flushBytes = 100 << 10
	b := txn.NewBatch()
	flush := func(force bool) error {
		if !force && b.ApproximateMutationBytes() < flushBytes {
			return nil
		}
		err := txn.Run(ctx, b)
		b = txn.NewBatch()
		return err
	}
	dst := tableSpan(uint32(dstID))
	if !keepExisting {
		rows, err := txn.Scan(ctx, dst.Key, dst.EndKey, 0)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if keep(roachpb.KeyValue{Key: row.Key, Value: *row.Value}) {
				b.Del(row.Key)
			}
		}
		if err := flush(true); err != nil {
			return err
		}
	}
	srcPrefix := codec.TablePrefix(uint32(srcID))
	dstPrefix := codec.TablePrefix(uint32(dstID))
	src := tableSpan(uint32(srcID))
	if _, err := storage.MVCCIterate(ctx, eng, src.Key, src.EndKey, target, storage.MVCCScanOptions{},
		func(kv roachpb.KeyValue) error {
			if !keep(kv) {
				return nil
			}
			key := append(dstPrefix[:len(dstPrefix):len(dstPrefix)], kv.Key[len(srcPrefix):]...)
			// The checksum covers the key, so a new one is made.
			v := roachpb.Value{RawBytes: append([]byte(nil), kv.Value.RawBytes...)}
			v.ClearChecksum()
			b.Put(key, &v)
			return flush(false)
		}); err != nil {
		return err
	}
	return flush(true)
}

// legacySchemaChangeJobs makes a job for each mutation of a restored table
// that the legacy schema changer was running at the target time, and for
// each table it was creating.
func legacySchemaChangeJobs(
	ctx context.Context, execCfg *sql.ExecutorConfig, txn descs.Txn, tables []*tabledesc.Mutable,
) error {
	sd := sql.NewInternalSessionData(ctx, execCfg.Settings, "restore")
	var records []*jobs.Record
	for _, tbl := range tables {
		if tbl.GetDeclarativeSchemaChangerState() != nil ||
			(len(tbl.MutationJobs) == 0 && !tbl.Adding()) {
			continue
		}
		// The backfill resumes from one span list per mutation, as the
		// legacy schema changer makes them.
		var spans []jobspb.ResumeSpanList
		for _, m := range tbl.Mutations {
			span := tbl.PrimaryIndexSpan(codec)
			if idx := m.GetIndex(); idx != nil && idx.UseDeletePreservingEncoding {
				span = tbl.IndexSpan(codec, idx.ID)
			}
			spans = append(spans, jobspb.ResumeSpanList{ResumeSpans: []roachpb.Span{span}})
		}
		record := func(mutationID descpb.MutationID) descpb.TableDescriptor_MutationJob {
			id := execCfg.JobRegistry.MakeJobID()
			records = append(records, &jobs.Record{
				JobID:         id,
				Description:   fmt.Sprintf("restored schema change on table %q", tbl.Name),
				Username:      username.NodeUserName(),
				DescriptorIDs: descpb.IDs{tbl.ID},
				Details: jobspb.SchemaChangeDetails{
					DescID:          tbl.ID,
					TableMutationID: mutationID,
					ResumeSpanList:  spans,
					FormatVersion:   jobspb.DatabaseJobFormatVersion,
					SessionData:     &sd.SessionData,
				},
				Progress: jobspb.SchemaChangeProgress{},
			})
			return descpb.TableDescriptor_MutationJob{MutationID: mutationID, JobID: id}
		}
		if tbl.Adding() {
			// This job makes the table public.
			record(descpb.InvalidMutationID)
		}
		tbl.MutationJobs = nil
		seen := map[descpb.MutationID]bool{}
		for _, m := range tbl.Mutations {
			if m.MutationID == descpb.InvalidMutationID || seen[m.MutationID] {
				continue
			}
			seen[m.MutationID] = true
			tbl.MutationJobs = append(tbl.MutationJobs, record(m.MutationID))
		}
	}
	_, err := execCfg.JobRegistry.CreateJobsWithTxn(ctx, txn, records)
	return err
}

// typeSchemaChangeJobs makes a job for each restored enum that the legacy
// type schema changer was adding or dropping values of at the target time.
func typeSchemaChangeJobs(
	ctx context.Context, execCfg *sql.ExecutorConfig, txn descs.Txn, all nstree.Catalog,
) error {
	var records []*jobs.Record
	if err := all.ForEachDescriptor(func(desc catalog.Descriptor) error {
		typ, ok := desc.(*typedesc.Mutable)
		if !ok || typ.GetDeclarativeSchemaChangerState() != nil {
			return nil
		}
		var transitioning [][]byte
		dropping := false
		for _, m := range typ.EnumMembers {
			if m.Capability != descpb.TypeDescriptor_EnumMember_ALL {
				transitioning = append(transitioning, m.PhysicalRepresentation)
				dropping = dropping || m.Direction == descpb.TypeDescriptor_EnumMember_REMOVE
			}
		}
		if len(transitioning) == 0 {
			return nil
		}
		records = append(records, &jobs.Record{
			JobID:         execCfg.JobRegistry.MakeJobID(),
			Description:   fmt.Sprintf("restored schema change on type %q", typ.Name),
			Username:      username.NodeUserName(),
			DescriptorIDs: descpb.IDs{typ.ID},
			Details: jobspb.TypeSchemaChangeDetails{
				TypeID:               typ.ID,
				TransitioningMembers: transitioning,
			},
			Progress:      jobspb.TypeSchemaChangeProgress{},
			NonCancelable: !dropping,
		})
		return nil
	}); err != nil {
		return err
	}
	_, err := execCfg.JobRegistry.CreateJobsWithTxn(ctx, txn, records)
	return err
}

// ttlSchedules makes a new row-level TTL schedule for each restored table
// that has a TTL, since schedules are not restored.
func ttlSchedules(
	ctx context.Context, execCfg *sql.ExecutorConfig, txn descs.Txn, tables []*tabledesc.Mutable,
) error {
	for _, tbl := range tables {
		if !tbl.HasRowLevelTTL() {
			continue
		}
		sj, err := sql.CreateRowLevelTTLScheduledJob(ctx, execCfg.JobsKnobs(), jobs.ScheduledJobTxn(txn),
			tbl.GetPrivileges().Owner(), tbl, execCfg.NodeInfo.LogicalClusterID(),
			execCfg.Settings.Version.ActiveVersion(ctx))
		if err != nil {
			return err
		}
		tbl.RowLevelTTL.ScheduleID = sj.ScheduleID()
	}
	return nil
}

func init() {
	jobs.RegisterConstructor(jobspb.TypeClusterRestore,
		func(job *jobs.Job, _ *cluster.Settings) jobs.Resumer {
			return &restoreResumer{job: job}
		},
		jobs.UsesTenantCostControl,
	)
}
