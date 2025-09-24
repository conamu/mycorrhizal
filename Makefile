generate:
	- protoc -I=schema --proto_path=schema --go_out=internal/ schema/*.proto
	#- flatc --go --go-namespace schema -o internal/ schema/*.fbs

