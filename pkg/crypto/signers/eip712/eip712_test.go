package eip712_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"stream.place/streamplace/pkg/crypto/signers/eip712"
	"stream.place/streamplace/pkg/crypto/signers/eip712/eip712test"
	v0 "stream.place/streamplace/pkg/schema/v0"
)

func TestEIP712Map(t *testing.T) {
	msg := eip712.StreamplaceEIP712Message{
		MsgData:   map[string]string{"foo": "bar"},
		MsgSigner: "0x295481766f43bb048aec5d71f3bf76fdacea78f2",
		MsgTime:   time.Now().UnixMilli(),
	}
	m := msg.Map()
	require.Equal(t, m["signer"], msg.MsgSigner)
}

func TestCreateSigner(t *testing.T) {
	ran := false
	eip712test.WithTestSigner(func(signer *eip712.EIP712Signer) {
		ran = true
	})
	require.True(t, ran)
}

func TestSignIdentity(t *testing.T) {
	eip712test.WithTestSigner(func(signer *eip712.EIP712Signer) {
		identity := v0.Identity{
			Handle: "aquareum.bsky.social",
			DID:    "did:plc:dkh4rwafdcda4ko7lewe43ml",
		}
		_, err := signer.SignMessage(identity)
		require.NoError(t, err)
	})
}

// var testCase = `{
//   "primaryType": "Identity",
//   "domain": {
//     "name": "Aquareum",
//     "version": "0.0.1",
//     "chainId": null,
//     "verifyingContract": "",
//     "salt": ""
//   },
//   "message": {
//     "signer": "0x9153c114d47aceb691b77b02122cb378074e45c8",
//     "time": 1732561417949,
//     "data": {
//       "handle": "aquareum.bsky.social",
//       "did": "did:plc:dkh4rwafdcda4ko7lewe43ml"
//     }
//   },
//   "signature": "0xc75ca7da2d110c562eaa4a906aae7a246b2d96a867b74baf3ac0d9127f260dfb17b9aba7d20562c10c771b658c17a4be2dfc427c3e729a07853e35753a8a70f61b"
// }`

// func TestVerifyIdentity(t *testing.T) {
// 	eip712test.WithTestSigner(func(signer *eip712.EIP712Signer) {
// 		signed, err := signer.Verify([]byte(testCase))
// 		require.NoError(t, err)
// 		require.Equal(t, signed.Signer(), "0x9153c114d47aceb691b77b02122cb378074e45c8")
// 		require.Equal(t, signed.Time(), int64(1732561417949))
// 		identity, ok := signed.Data().(*v0.Identity)
// 		require.True(t, ok)
// 		require.Equal(t, identity.Handle, "aquareum.bsky.social")
// 		require.Equal(t, identity.DID, "did:plc:dkh4rwafdcda4ko7lewe43ml")
// 	})
// }

// func TestFailingGoLive(t *testing.T) {
// 	eip712test.WithTestSigner(func(signer *eip712.EIP712Signer) {
// 		failingTestCase := strings.Replace(testCase, "aquareum.bsky.social", "evilhandle.evil", 1)
// 		_, err := signer.Verify([]byte(failingTestCase))
// 		require.Error(t, err)
// 	})
// }

// Use a fresh process so other tests cannot hide retained startup scratch space.
func TestKeystoreStartup(t *testing.T) {
	if os.Getenv("STREAMPLACE_KEYSTORE_STARTUP_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestKeystoreStartup$")
		cmd.Env = append(os.Environ(), "STREAMPLACE_KEYSTORE_STARTUP_TEST=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	path := t.TempDir()
	opts := &eip712.EIP712SignerOptions{EthKeystorePath: path, EthKeystorePassword: "startup-test-password"}
	signer := &eip712.EIP712Signer{Opts: opts}
	require.NoError(t, signer.InitKeystore(context.Background()))
	hash := crypto.Keccak256([]byte("keystore startup"))
	signature, err := signer.EthSign(hash)
	require.NoError(t, err)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	require.Less(t, memory.HeapSys-memory.HeapReleased, uint64(64<<20), "unused scratch pages must be returned to the OS")
	files, err := os.ReadDir(path)
	require.NoError(t, err)
	require.Len(t, files, 1)
	keyPath := filepath.Join(path, files[0].Name())
	info, err := os.Stat(keyPath)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0077, "encrypted key must be private")
	data, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	var encrypted struct {
		Version int                 `json:"version"`
		Crypto  keystore.CryptoJSON `json:"crypto"`
	}
	require.NoError(t, json.Unmarshal(data, &encrypted))
	require.Equal(t, 3, encrypted.Version)
	require.Equal(t, "aes-128-ctr", encrypted.Crypto.Cipher)
	require.Equal(t, "scrypt", encrypted.Crypto.KDF)
	require.Equal(t, float64(keystore.StandardScryptN), encrypted.Crypto.KDFParams["n"])
	require.Equal(t, float64(keystore.StandardScryptP), encrypted.Crypto.KDFParams["p"])
	require.Equal(t, float64(8), encrypted.Crypto.KDFParams["r"])
	require.Equal(t, float64(32), encrypted.Crypto.KDFParams["dklen"])

	restarted := &eip712.EIP712Signer{Opts: opts}
	require.NoError(t, restarted.InitKeystore(context.Background()))
	require.Equal(t, signer.Hex(), restarted.Hex())
	restartedSignature, err := restarted.EthSign(hash)
	require.NoError(t, err)
	require.Equal(t, signature, restartedSignature)
	runtime.ReadMemStats(&memory)
	require.Less(t, memory.HeapSys-memory.HeapReleased, uint64(64<<20), "unlock must return unused scratch pages to the OS")
	wrongOpts := *opts
	wrongOpts.EthKeystorePassword = "wrong-password"
	wrong := &eip712.EIP712Signer{Opts: &wrongOpts}
	require.ErrorIs(t, wrong.InitKeystore(context.Background()), keystore.ErrDecrypt)
	require.Nil(t, wrong.KeyStore)
	require.Nil(t, wrong.Account)
	runtime.ReadMemStats(&memory)
	require.Less(t, memory.HeapSys-memory.HeapReleased, uint64(64<<20), "failed unlock must also release scratch pages")
	unchanged, err := os.ReadFile(keyPath)
	require.NoError(t, err)
	require.Equal(t, data, unchanged, "failed unlock must not rewrite the protected key")
	runtime.KeepAlive(signer)
	runtime.KeepAlive(restarted)
}
