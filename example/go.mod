module github.com/struct0x/initd/example

go 1.25.5

require (
	github.com/struct0x/initd v0.0.0
	github.com/struct0x/initd/initdhttp v0.0.0
)

require (
	github.com/struct0x/envconfig v1.3.0 // indirect
	github.com/struct0x/exitplan v1.1.0 // indirect
)

replace (
	github.com/struct0x/initd => ../
	github.com/struct0x/initd/initdhttp => ../initdhttp
)
