package saastest

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"sync"
)

// testKeyDER is a 2048-bit RSA key made for these tests only (PKCS#1 DER, base64). It signs
// nothing real and is never a credential: the JWT goldens need one fixed key, because RS256
// signatures are deterministic for a fixed key and input.
const testKeyDER = "MIIEogIBAAKCAQEA/LDm1goMcuE8S+xayvDoskxRCoTGTmLkL9ndjPefLa4MxPiKV2Q5Q1nsX0vWxfucizp/HtFvUSDX48xy2iP0AJqw/hHTpjYlpVnYnKi7Aru6ccAk4l4W0ikanL+ElytE6E9G4UMKQdAMCJiN+HfLwfewWFU0ps+36sq7FUDIm2EIzcZx5+m/CJYrmtaxkamOokvlzDnlBLxYonJTVf2ja5QTL+2OMalElrCJXtNcN6X33UOOd+PDTz46btSrTPIfUmeM33B0e3HxN4KrtXROLumqh32tZRIwUdu/VrUBbGs5oVUNEPNuD4AivmMsP5isrQe1hRShm5TWGCCOGeMNWQIDAQABAoIBAAUgZi/GECdG7VWHajokRpxL2ZnLsEdtGsWSfgy+Vrc2nTWMp4Xb5RMYcWSdctPnSrmGL4fJCCELNNOMs8HQNt3J2dqDMxNZATILuyWk0feF4yBUkBG+DYOURmEFrqAYFz3Fvi8aCaRC9rv09rE9ttiip9R6G771DiXTfeaK+6OOD4vtz5apGp1807+G8iS3ebIv5ptF6BMtUlqm2jl84mij2K+//aYab26IWwrOYcQcO1mfq474JmntRlVBeuPjpT5HfQyIjGEqvx+iGCgeE7+E66Vdpyn1ievOBFD1plQFloBv+Hwy/6hQnOvuCXZQdGGubhM5PD+N82hqWH/X6UcCgYEA/ewYCPjG59PPFxs43IU21KJczHWf6CNhmk2y3bhx2Xh78TYrEC+xUvO3F8nVM3Owk7Lmp76HDo4MrFbvhVhfAtpa55yZhg4aUwnMQEG2LEiZ9RevWPVUFRBKTk7uNA64aehO476bhCZ27DYrKGRzfAprV5RL+op9ktsRFbdccEcCgYEA/sI6jJMM7exakyVW6OjZ7cBKooyXPtv31WUo9r+J2l3K3fXroPc3PeIGqetCcNJo010p7f5Pv1g76Dsa1VJ50AAHLl/k5aKiyehtgNvtQl6R3AVhkLW2J4XHeIiloqFHx+jyM/hCQ/pnUEx1de2dVOhV+ad9D7diqfKxGjJ8BV8CgYByd8Q+ByhjyfBJklQH1WXqZWdAJKZ5l7mgPfasTUHFr7LwAmNXYKinpIadirc6AqVPcA7a/+EShqbf6jEAA96RAq6ISuhKroDffkM9lGHzhKci/hJmH/JPrJBSzPJFNX2dAMTPllW1NUHXKnQ6OU52VJwfVxMJVvVVBryk9YwOcQKBgAGHFqTLKPOwTCa/vZKt+siTM59f6nRFC2prLeC061W05EeKdvxDH0Cuv017jK1yhZBm6Z50vPOvysQsvo9y7CoUJfbgOlYqMIG3dBimuPiB6qwAGEKu5EW+ZqDRaPTdtLl9Kx6mqXcQ5Fe17apkX2In9oQFvhk/B0dvniJDFT1PAoGAWSEl2YK2xIDfGxhdDxmydzT/aRaFZPKw1J31/FadVqN8DUlQlQzAh/goWpV2gCUMd8jredCgaIkervuQ9I6o9dyeWRBadO37FkXiK9sZbxQi89IXwVULZwdLMDf3vCsKBVV+NTeKI5dgcSpchq1MHGTLrurpPLGOrUZwF4Ht6SU="

var (
	keyOnce sync.Once
	key     *rsa.PrivateKey
)

// TestRSAKey returns the fixed test key.
func TestRSAKey() *rsa.PrivateKey {
	keyOnce.Do(func() {
		der, err := base64.StdEncoding.DecodeString(testKeyDER)
		if err != nil {
			panic(err)
		}
		k, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			panic(err)
		}
		key = k
	})
	return key
}

// TestRSAKeyPEM returns the fixed test key as a PKCS#1 PEM block (what a GitHub App's private key
// file looks like), built at run time so no PEM block sits in the repository.
func TestRSAKeyPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(TestRSAKey())})
}
