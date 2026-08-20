module github.com/subosito/mow

go 1.26.4

require (
	github.com/creack/pty v1.1.24
	github.com/subosito/mow/packs v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/kr/text v0.2.0 // indirect

replace (
	github.com/subosito/mow/packs => ./packs
	github.com/subosito/mow/packs/otel => ./packs/otel
)
