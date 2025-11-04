package plugin

type Interface interface {
	IsSupported() bool
	GetVersion() int32
	GetToken() []byte
	Deobfuscate(obfuscated, fileId []byte) ([]byte, error)
}
