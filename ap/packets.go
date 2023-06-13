package ap

//go:generate go run golang.org/x/tools/cmd/stringer -type=PacketType
type PacketType byte

const (
	PacketTypeSecretBlock     PacketType = 0x02
	PacketTypePing            PacketType = 0x04
	PacketTypeStreamChunk     PacketType = 0x08
	PacketTypeStreamChunkRes  PacketType = 0x09
	PacketTypeChannelError    PacketType = 0x0a
	PacketTypeChannelAbort    PacketType = 0x0b
	PacketTypeRequestKey      PacketType = 0x0c
	PacketTypeAesKey          PacketType = 0x0d
	PacketTypeAesKeyError     PacketType = 0x0e
	PacketTypeImage           PacketType = 0x19
	PacketTypeCountryCode     PacketType = 0x1b
	PacketTypePong            PacketType = 0x49
	PacketTypePongAck         PacketType = 0x4a
	PacketTypePause           PacketType = 0x4b
	PacketTypeProductInfo     PacketType = 0x50
	PacketTypeLegacyWelcome   PacketType = 0x69
	PacketTypeLicenseVersion  PacketType = 0x76
	PacketTypeLogin           PacketType = 0xab
	PacketTypeAPWelcome       PacketType = 0xac
	PacketTypeAuthFailure     PacketType = 0xad
	PacketTypeMercuryReq      PacketType = 0xb2
	PacketTypeMercurySub      PacketType = 0xb3
	PacketTypeMercuryUnsub    PacketType = 0xb4
	PacketTypeMercuryEvent    PacketType = 0xb5
	PacketTypeTrackEndedTime  PacketType = 0x82
	PacketTypePreferredLocale PacketType = 0x74
	PacketTypeUnknown1f       PacketType = 0x1f
	PacketTypeUnknown4f       PacketType = 0x4f
	PacketTypeUnknown0f       PacketType = 0x0f
	PacketTypeUnknown10       PacketType = 0x10
)
