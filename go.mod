module github.com/cockroachdb/cockroach

go 1.27.4

// Tests seed math/rand with randutil.SeedForTests, so that the logged seed
// reproduces a failure. Since Go 1.24, rand.Seed does nothing unless
// randseednop is 0.
godebug randseednop=0

require (
	cloud.google.com/go/kms v1.37.0
	cloud.google.com/go/storage v1.71.0
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.23.3
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.15.0
	github.com/Azure/azure-sdk-for-go/sdk/keyvault/azkeys v0.9.0
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v0.6.1
	github.com/Azure/go-autorest/autorest v0.11.20
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c
	github.com/NYTimes/gziphandler v0.0.0-20170623195520-56545f4a5d46
	github.com/PuerkitoBio/goquery v1.5.1
	github.com/VividCortex/ewma v1.1.1
	github.com/andy-kimball/arenaskl v0.0.0-20200617143215-f701008588b9
	github.com/apache/arrow/go/arrow v0.0.0-20200923215132-ac86123a3f01
	github.com/apache/arrow/go/v11 v11.0.0
	github.com/aws/aws-sdk-go-v2 v1.48.0
	github.com/aws/aws-sdk-go-v2/config v1.33.21
	github.com/aws/aws-sdk-go-v2/credentials v1.21.3
	github.com/aws/aws-sdk-go-v2/feature/s3/manager v1.23.28
	github.com/aws/aws-sdk-go-v2/service/kms v1.63.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.116.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.53.0
	github.com/aws/smithy-go v1.28.9
	github.com/axiomhq/hyperloglog v0.0.0-20181223111420-4b99d0c2c99e
	github.com/biogo/store v0.0.0-20160505134755-913427a1d5e8
	github.com/blevesearch/snowballstem v0.9.0
	github.com/charmbracelet/bubbles v0.15.1-0.20230123181021-a6a12c4a31eb
	github.com/cockroachdb/apd/v3 v3.2.1
	github.com/cockroachdb/cmux v0.0.0-20170110192607-30d10be49292
	github.com/cockroachdb/cockroach-go/v2 v2.3.5
	github.com/cockroachdb/datadriven v1.0.3-0.20230801171734-e384cf455877
	github.com/cockroachdb/errors v1.11.1
	github.com/cockroachdb/fifo v0.0.0-20240606204812-0bbfbd93a7ce
	github.com/cockroachdb/gostdlib v1.19.0
	github.com/cockroachdb/logtags v0.0.0-20230118201751-21c54148d20b
	github.com/cockroachdb/pebble v1.1.2-0.20241107030133-19b47dc92d30
	github.com/cockroachdb/redact v1.1.5
	github.com/cockroachdb/returncheck v0.0.0-20200612231554-92cdbca611dd
	github.com/cockroachdb/tokenbucket v0.0.0-20230807174530-cc333fc44b06
	github.com/cockroachdb/ttycolor v0.0.0-20210902133924-c7d7dcdde4e8
	github.com/codahale/hdrhistogram v0.0.0-20161010025455-3a0bb77429bd
	github.com/dave/dst v0.24.0
	github.com/davecgh/go-spew v1.1.1
	github.com/dustin/go-humanize v1.0.0
	github.com/edsrzf/mmap-go v1.0.0
	github.com/elastic/gosigar v0.14.1
	github.com/emicklei/dot v0.15.0
	github.com/fraugster/parquet-go v0.10.0
	github.com/fsnotify/fsnotify v1.5.1
	github.com/ghemawat/stream v0.0.0-20171120220530-696b145b53b9
	github.com/go-openapi/strfmt v0.27.0
	github.com/go-sql-driver/mysql v1.6.0
	github.com/gogo/protobuf v1.3.2
	github.com/gogo/status v1.1.0
	github.com/golang-commonmark/markdown v0.0.0-20180910011815-a8f139058164
	github.com/golang/mock v1.6.0
	github.com/golang/protobuf v1.5.4
	github.com/golang/snappy v0.0.4
	github.com/google/btree v1.0.1
	github.com/google/flatbuffers v2.0.8+incompatible
	github.com/google/go-cmp v0.7.0
	github.com/google/pprof v0.0.0-20210827144239-02619b876842
	github.com/google/uuid v1.6.0
	github.com/googleapis/gax-go/v2 v2.27.0
	github.com/gorilla/mux v1.8.0
	github.com/grpc-ecosystem/grpc-gateway v1.16.0
	github.com/guptarohit/asciigraph v0.5.5
	// pgx 5.11 and later parse URLs like libpq: they reject "=" inside a
	// parameter such as options=-ccluster=x and read "+" as itself, which
	// breaks the URLs that CockroachDB documents.
	github.com/jackc/pgx/v5 v5.10.0
	github.com/jellevandenhooff/gosim v0.0.0
	github.com/jordanlewis/gcassert v0.0.0-20230505190637-fed79d91cd5f
	github.com/kisielk/errcheck v1.9.0
	github.com/kisielk/gotool v1.0.0
	github.com/klauspost/compress v1.20.4
	github.com/knz/bubbline v0.0.0-20230422210153-e176cdfe1c43
	github.com/kr/pretty v0.3.1
	github.com/kr/text v0.2.0
	github.com/kylelemons/godebug v1.1.0
	github.com/leanovate/gopter v0.2.5-0.20190402064358-634a59d12406
	github.com/lib/pq v1.10.7
	github.com/linkedin/goavro/v2 v2.12.0
	github.com/lufia/iostat v1.2.1
	github.com/maruel/panicparse/v2 v2.2.2
	github.com/marusama/semaphore v0.0.0-20190110074507-6952cef993b2
	github.com/mattn/go-isatty v0.0.20
	github.com/mitchellh/reflectwalk v1.0.0
	github.com/mkungla/bexp/v3 v3.0.1
	github.com/montanaflynn/stats v0.7.1
	github.com/mozillazg/go-slugify v0.2.0
	github.com/nightlyone/lockfile v1.0.0
	github.com/olekukonko/tablewriter v0.0.5-0.20200416053754-163badb3bac6
	github.com/petermattis/goid v0.0.0-20211229010228-4d14c490ee36
	github.com/pierrec/lz4/v4 v4.1.21
	github.com/pkg/errors v0.9.1
	github.com/pmezard/go-difflib v1.0.0
	github.com/pressly/goose/v3 v3.5.3
	github.com/prometheus/client_golang v1.21.1
	github.com/prometheus/client_model v0.6.2
	github.com/prometheus/common v0.63.0
	github.com/rcrowley/go-metrics v0.0.0-20201227073835-cf1acfcdf475
	github.com/robfig/cron/v3 v3.0.1
	github.com/rs/dnscache v0.0.0-20230804202142-fc85eb664529
	github.com/sasha-s/go-deadlock v0.3.1
	github.com/shirou/gopsutil/v3 v3.21.12
	github.com/spf13/afero v1.15.0
	github.com/spf13/cobra v1.2.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.12.1
	github.com/xdg-go/pbkdf2 v1.0.0
	github.com/xdg-go/scram v1.1.2
	github.com/xdg-go/stringprep v1.0.4
	go.etcd.io/raft/v3 v3.0.0-20240612143628-49a8a628fa47
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.47.0
	go.opentelemetry.io/otel/exporters/zipkin v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	golang.org/x/crypto v0.59.0
	golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa
	golang.org/x/net v0.63.0
	golang.org/x/oauth2 v0.38.0
	golang.org/x/sync v0.25.0
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.48.0
	golang.org/x/text v0.44.0
	golang.org/x/time v0.16.0
	golang.org/x/tools v0.53.0
	google.golang.org/api v0.316.0
	google.golang.org/genproto/googleapis/api v0.0.0-20261110000943-09e21b237f2c
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.1
	vitess.io/vitess v0.0.0-00010101000000-000000000000
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.4 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.1 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/iam v1.15.1 // indirect
	cloud.google.com/go/longrunning v1.5.0 // indirect
	cloud.google.com/go/monitoring v1.30.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/keyvault/internal v0.7.0 // indirect
	github.com/Azure/go-autorest v14.2.0+incompatible // indirect
	github.com/Azure/go-autorest/autorest/adal v0.9.15 // indirect
	github.com/Azure/go-autorest/autorest/date v0.3.0 // indirect
	github.com/Azure/go-autorest/logger v0.2.1 // indirect
	github.com/Azure/go-autorest/tracing v0.6.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.10.1 // indirect
	github.com/DataDog/zstd v1.5.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.62.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.62.0 // indirect
	github.com/JohnCGriffin/overflow v0.0.0-20211019200055-46fa312c352c // indirect
	github.com/Masterminds/goutils v1.1.1 // indirect
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/Masterminds/sprig v2.22.0+incompatible // indirect
	github.com/aclements/go-moremath v0.0.0-20210112150236-f10218a38794 // indirect
	github.com/andybalholm/brotli v1.3.0 // indirect
	github.com/andybalholm/cascadia v1.2.0 // indirect
	github.com/apache/thrift v0.24.0 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.27 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.14.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.5 // indirect
	github.com/aymanbagabas/go-osc52 v1.0.3 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/bubbletea v0.23.1 // indirect
	github.com/charmbracelet/lipgloss v0.6.0 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/cockroachdb/crlfmt v0.0.0-20221214225007-b2fc5c302548 // indirect
	github.com/containerd/console v1.0.3 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.1 // indirect
	github.com/dgryski/go-metro v0.0.0-20180109044635-280f6062b5bc // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.39.1-0.20260819172001-e6e3fd93e4be // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/getsentry/sentry-go v0.23.0 // indirect
	github.com/ghodss/yaml v1.0.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-openapi/errors v0.22.8 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.9.11 // indirect
	github.com/gogo/googleapis v1.4.1 // indirect
	github.com/golang-commonmark/html v0.0.0-20180910111043-7d7c804e1d46 // indirect
	github.com/golang-commonmark/linkify v0.0.0-20180910111149-f05efb453a0e // indirect
	github.com/golang-commonmark/mdurl v0.0.0-20180910110917-8d018c6567d6 // indirect
	github.com/golang-commonmark/puny v0.0.0-20180910110745-050be392d8b8 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/glog v1.2.5 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.25 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/huandu/xstrings v1.3.0 // indirect
	github.com/ianlancetaylor/demangle v0.0.0-20200824232613-28f6c0f3b639 // indirect
	github.com/imdario/mergo v0.3.13 // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/asmfmt v1.3.2 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.14 // indirect
	github.com/minio/asm2plan9s v0.0.0-20200509001527-cdd76441f9d8 // indirect
	github.com/minio/c2goasm v0.0.0-20190812172519-36a3d3bbc4f3 // indirect
	github.com/mitchellh/copystructure v1.0.0 // indirect
	github.com/mmatczuk/go_generics v0.0.0-20181212143635-0aaa050f9bab // indirect
	github.com/mozillazg/go-unidecode v0.2.0 // indirect
	github.com/muesli/ansi v0.0.0-20211031195517-c9f0611b6c70 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/reflow v0.3.0 // indirect
	github.com/muesli/termenv v0.13.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/mwitkow/go-proto-validators v0.0.0-20180403085117-0950a7990007 // indirect
	github.com/oklog/ulid/v2 v2.1.1 // indirect
	github.com/opennota/wd v0.0.0-20180911144301-b446539ab1e7 // indirect
	github.com/openzipkin/zipkin-go v0.4.3 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c // indirect
	github.com/prometheus/procfs v0.16.0 // indirect
	github.com/pseudomuto/protoc-gen-doc v1.3.2 // indirect
	github.com/pseudomuto/protokit v0.2.0 // indirect
	github.com/rivo/uniseg v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/russross/blackfriday v1.6.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/sahilm/fuzzy v0.1.0 // indirect
	github.com/spiffe/go-spiffe/v2 v2.8.1 // indirect
	github.com/tklauser/go-sysconf v0.3.9 // indirect
	github.com/tklauser/numcpus v0.3.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.2 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.47.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	// The indicated commit is required on top of v1.0.0-RC3 because
	// it fixes an import comment that otherwise breaks our prereqs tool.
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.43.0 // indirect
	golang.org/x/perf v0.0.0-20230113213139-801c7ef9e5c5 // indirect
	golang.org/x/telemetry v0.0.0-20261116191925-e77a43154acb // indirect
	golang.org/x/xerrors v0.0.0-20220907171357-04be3eba64a2 // indirect
	google.golang.org/genproto v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261110000943-09e21b237f2c // indirect
	honnef.co/go/tools v0.8.2 // indirect
)

// Until this PR is merged: https://github.com/charmbracelet/bubbletea/pull/397
replace github.com/charmbracelet/bubbletea => github.com/cockroachdb/bubbletea v0.23.1-bracketed-paste2

replace github.com/olekukonko/tablewriter => github.com/cockroachdb/tablewriter v0.0.5-0.20200105123400-bd15540e8847

replace github.com/abourget/teamcity => github.com/cockroachdb/teamcity v0.0.0-20180905144921-8ca25c33eb11

replace vitess.io/vitess => github.com/cockroachdb/vitess v0.0.0-20210218160543-54524729cc82

replace gopkg.in/yaml.v2 => github.com/cockroachdb/yaml v0.0.0-20210825132133-2d6955c8edbc

replace golang.org/x/time => github.com/cockroachdb/x-time v0.3.1-0.20230525123634-71747adb5d5c

// Take etcd-io/raft from the branch corresponding to release-23.2 in our fork.
replace go.etcd.io/raft/v3 => github.com/cockroachdb/raft/v3 v3.0.0-20240612143628-49a8a628fa47

tool (
	github.com/cockroachdb/crlfmt
	github.com/cockroachdb/gostdlib/x/tools/cmd/goimports
	github.com/golang/mock/mockgen
	github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway
	github.com/mmatczuk/go_generics/cmd/go_generics
	github.com/pseudomuto/protoc-gen-doc/cmd/protoc-gen-doc
	golang.org/x/tools/cmd/goyacc
	honnef.co/go/tools/cmd/staticcheck
)

replace github.com/jellevandenhooff/gosim => github.com/djosh34/gosim v0.0.0-20261121202200-a06b7e7ff5c6
