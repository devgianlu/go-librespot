package go_librespot

//go:generate go run github.com/bufbuild/buf/cmd/buf@v1.66.0 generate
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.0 --config=api-codegen.yml api-spec.yml
//go:generate go run github.com/vektra/mockery/v3@v3.5.5
