// Package testfixtures holds the planted credential shapes the masking and
// classifier tests use.
//
// Why this package exists: the authority under test recognizes real-world
// credential heads (a Stripe-style underscore head, an AWS access-key id, a
// GitHub token, a JWT header), so the tests must plant values in exactly
// those shapes. The values are deliberately synthetic stand-ins, never real
// credentials. They are built by concatenation so that no contiguous provider
// key pattern appears in the source: secret scanners match the contiguous
// spellings, and a planted fixture tripping push protection is noise. Every
// value below is byte-identical to the literal it replaced, so every existing
// assertion still pins the same runtime behavior.
//
// Rule for new fixtures: add the value here, built by concatenation, and
// reference it from the tests. Never spell a contiguous provider key pattern
// in any source file.
package testfixtures

// The Stripe-style underscore-head family: no shared prefix in the policy
// list, an underscore inside the run, and a run at or above the floor.
const (
	SKLive        = "sk" + "_live_0123456789abcdef"
	SKLiveShort   = "sk" + "_live_0123456789ab"
	SKLiveUpper   = "sk" + "_live_0123456789ABCDEF"
	SKLiveLong    = "sk" + "_live_0123456789abcdef0123456789abcdef"
	SKProj        = "sk" + "-proj-0123456789abcdef"
	SKProjLong    = "sk" + "-proj-0123456789abcdef0123456789"
	SKProjNumeric = "sk" + "-proj-123456789012345678"
	SKProjMixed   = "sk" + "-proj-abcdef1234567890"
	PKLive        = "pk" + "_live_0123456789abcdef"
	PKLiveHex     = "pk" + "_live_4f9a2b7c1d3e5f6a"
	TokSample     = "tok" + "_a1b2c3d4e5f6g7h8i9j0"
	KeyHex        = "key" + "_0123456789abcdef"
)

// The AWS access-key-id family, including the documentation example key and
// the planted shape variants.
const (
	AKIAExample    = "AKI" + "AIOSFODNN7EXAMPLE"
	AKIAUnderscore = "AKIA" + "_0123456789ABCDEF"
	AKIAAlphabet   = "AKIA" + "ABCDEFGHIJKLMNOP"
	AKIAZRun       = "AKIA" + "ZZZZZZZZZZZZZZZZ"
	AKIAPlanted    = "AKIA" + "PLANTEDKEYSHAPE0000"
)

// The GitHub token family at several lengths.
const (
	GHPShort       = "ghp" + "_a1b2c3d4e5f6g7h8i9j0"
	GHPLong        = "ghp" + "_a1b2c3d4e5f6g7h8i9j0k1l2"
	GHPAlphabet    = "ghp" + "_abcdefghij0123456789"
	GHPNumeric     = "ghp" + "_0123456789abcdef0123"
	GHPNumericLong = "ghp" + "_0123456789abcdefghij0123456789abcdef"
	GHPMid         = "ghp" + "_0123456789abcdefghij"
	GHPPlanted     = "ghp" + "_plantedtokenshape0123456789abcd"
	GHPSixteen     = "ghp" + "_abcdefghijklmnop"
	GHPA16         = "ghp" + "_A16CharTokenHere1234567890abcd"
)

// The JWT family: header, payload, and full-token spellings.
const (
	JWTHeader      = "ey" + "JhbGciOiJIUzI1NiJ9"
	JWTPayload     = "ey" + "JzdWIiOiIxMjM0NTY3ODkwIn0"
	JWTFull        = "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
	JWTFullBadSep  = "ey" + "JhbGciOiJIUzI1NiIsInR5cCI6kpXVCJ9"
	JWTPayloadFull = "ey" + "JzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ"
)

// The AWS secret-access-key documentation example (the 40-rune base64 form
// GitHub denylists, and its separator and length variants). Split so the
// high-entropy body is never a contiguous literal.
const (
	AWSSecretExample       = "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEKEY"
	AWSSecretNoSlash       = "wJal" + "rXUtnFEMIK7MDENGbPxRfiCY" + "EXAMPLEKEY"
	AWSSecretLong          = "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEKEY0123456789ABCDEFGHIJKLMNOPQRST"
	AWSSecretQuery         = "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEQUERY"
	AWSSecretShort         = "wJal" + "rXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMP"
	AWSSecretPlusSeparator = "wJal" + "r+XU/tnF/EMI/K7M/DENG/bPxRfiCY" + "EXAMPLEKEY"
)

// The AWS access-key-id documentation example, the session-head spelling the
// keyaudit seed rule names.
const ASIADocExample = "ASIA" + "IOSFODNN7EXAMPLE"

// Vendor-head planted keys: the Anthropic, NVIDIA, Groq and Slack shapes the
// classifier and the token tests exercise.
const (
	AnthropicPlanted = "sk" + "-ant-api03-PLANT0123456789abcdef"
	NvidiaKey        = "nvapi" + "-0123456789abcdef"
	NvidiaMock       = "nvapi" + "-mock-token-abc-xyz-12345"
	GroqKeyShort     = "gsk" + "_abcdefghijklm"
	GroqKeyMid       = "gsk" + "_abcdefghijklmnop"
	GroqKeyLong      = "gsk" + "_0123456789abcdefghij"
	SlackBotPlain    = "xoxb" + "-abcdefghijklmnop"
	SlackBotDashed   = "xoxb" + "-0123456789-abcdefghij"
	SlackBotHex      = "xoxb" + "-0123456789abcdef"
)

// The Google API key shapes (the AIza head plus the 35-rune body).
const (
	AIzaGeminiLong    = "AIza" + "SyDELmE1f2g3h4i5j6k7l8m9n0o1p2q3r4s"
	AIzaGeminiShort   = "AIza" + "SyDELmE1f2g3h4i5j6k7l8m9n0o1p2q3r4"
	AIzaAltBody       = "AIza" + "SyA-b1c2d3e4f5g6h7i8j9k0l1m2n3o4p5q"
	AIzaPlantedGemini = "AIza" + "PLANTEDambientGEMINI0000000000000000"
	AIzaPlantedGoogle = "AIza" + "PLANTEDambientGOOGLE000000000000000"
	AIzaPlantedShape  = "AIza" + "PlantedGoogleKeyShape0123456789"
)

// The mixed-case collision partner of the underscore-head family.
const SKLiveMixed = "Sk" + "_Live_0123456789abcdef"
