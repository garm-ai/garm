package compiler

// ConnectNames mirrors protoc-gen-connect-go's default output naming.
//
// protoc-gen-connect-go (see connectrpc.com/connect/cmd/protoc-gen-connect-go,
// generate() and newNames()) with its default package_suffix="connect":
//   - appends the suffix to the FILE's Go package name to get the connect
//     package name (file.GoPackageName += "connect"), and generates into a
//     subdirectory of the same name;
//   - names the server interface "<ServiceGoName>Handler".
//
// This is a coupling, not a contract: connect-go decides these names and we
// reproduce them here in order to reference its generated interfaces without
// generating them ourselves. TestConnectNamingMirror pins the values against
// the real generated output checked into this repo, so a connect-go upgrade
// that changes this naming fails loudly here instead of as a compile error
// in every consumer's generated code.
func ConnectNames(goPackageName, serviceName string) (pkgName, handlerIface string) {
	return goPackageName + "connect", serviceName + "Handler"
}
