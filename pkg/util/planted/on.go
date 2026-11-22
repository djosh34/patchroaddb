// Copyright 2026 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License. You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under
// the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF
// ANY KIND, either express or implied. See the License for the specific language
// governing permissions and limitations under the License.

//go:build planted

package planted

import "github.com/cockroachdb/cockroach/pkg/util/envutil"

// Env names the planted bug that is on.
const Env = "PATCHROADDB_PLANTED"

// on is the bug that Env names, read once.
var on, _ = envutil.ExternalEnvString(Env, 0)

// On reports whether the planted bug is the one PATCHROADDB_PLANTED names.
func On(bug string) bool { return bug == on }
