package identity

// aos446_sshsig_test.go — os VECTORES GOLDEN da verificação FIDO2 (AOS-446 fase 1).
//
// # PORQUE É QUE ESTES BYTES SÃO DE CONFIANÇA
//
// O verificador foi escrito a partir de uma leitura do formato, e uma leitura do formato é uma
// hipótese. Os vectores de `testdata/aos446_sshsig_sk_vectores.json` não são: cada um foi gerado
// com esta serialização, escrito em disco com o armor do `ssh-keygen`, e entregue ao
// **`ssh-keygen -Y verify` do OpenSSH_10.3p1**, que os aceitou (o campo `openssh_veredicto` de
// cada vector regista-o). Com a mensagem trocada, a namespace trocada ou um byte do contador
// mutado, o mesmo `ssh-keygen` recusa-os. É por isso que estes bytes ancoram o verificador a uma
// implementação que não é a nossa, e não a si próprios.
//
// O que NÃO se pôde fazer, e fica dito: as chaves são ed25519 de software a FINGIR-SE de
// autenticador. Uma chave FIDO2 verdadeira exige hardware, que este ambiente não tem. A diferença
// entre as duas está toda do lado de quem ASSINA — o verificador não consegue distingui-las, e é
// suposto não conseguir: o que ele verifica é o blob, e o blob de um autenticador real tem esta
// forma exacta, que é precisamente o que o `ssh-keygen` acabou de confirmar.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type sshsigVector struct {
	Nome             string `json:"nome"`
	SeedHex          string `json:"seed_hex"`
	Application      string `json:"application"`
	Namespace        string `json:"namespace"`
	HashAlgorithm    string `json:"hash_algorithm"`
	Flags            byte   `json:"flags"`
	Counter          uint32 `json:"counter"`
	MensagemB64      string `json:"mensagem_b64"`
	ChaveBlobB64     string `json:"chave_blob_b64"`
	ChaveLinha       string `json:"chave_linha"`
	EnvelopeB64      string `json:"envelope_b64"`
	SSHFingerprint   string `json:"ssh_fingerprint"`
	OpenSSHVeredicto string `json:"openssh_veredicto"`
}

func carregarVectores(t *testing.T) map[string]sshsigVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/aos446_sshsig_sk_vectores.json")
	if err != nil {
		t.Fatalf("ler vectores: %v", err)
	}
	var vs []sshsigVector
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatalf("vectores ilegiveis: %v", err)
	}
	out := make(map[string]sshsigVector, len(vs))
	for _, v := range vs {
		if v.OpenSSHVeredicto == "" {
			t.Fatalf("vector %q sem veredicto do OpenSSH — um vector que ninguem de fora validou nao ancora nada", v.Nome)
		}
		out[v.Nome] = v
	}
	if len(out) != 4 {
		t.Fatalf("esperados 4 vectores, estao %d", len(out))
	}
	return out
}

func (v sshsigVector) envelope(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(v.EnvelopeB64)
	if err != nil {
		t.Fatalf("envelope do vector %q ilegivel: %v", v.Nome, err)
	}
	return b
}

func (v sshsigVector) mensagem(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(v.MensagemB64)
	if err != nil {
		t.Fatalf("mensagem do vector %q ilegivel: %v", v.Nome, err)
	}
	return b
}

func (v sshsigVector) pino(t *testing.T) SKPublicKey {
	t.Helper()
	k, err := ParseSKPublicKey(v.ChaveLinha)
	if err != nil {
		t.Fatalf("chave do vector %q: %v", v.Nome, err)
	}
	return k
}

// TestAOS446SSHSIGVectoresGolden — o verificador aceita exactamente o que o OpenSSH aceitou, e a
// impressão digital que calcula é a que o `ssh-keygen -lf` imprime.
func TestAOS446SSHSIGVectoresGolden(t *testing.T) {
	vs := carregarVectores(t)
	for _, nome := range []string{"sha512_up", "sha256_up_uv"} {
		v := vs[nome]
		t.Run(nome, func(t *testing.T) {
			pino := v.pino(t)
			if pino.SSHFingerprint() != v.SSHFingerprint {
				t.Fatalf("impressao digital %q, esperada %q (a do ssh-keygen -lf)", pino.SSHFingerprint(), v.SSHFingerprint)
			}
			if pino.Application != v.Application {
				t.Fatalf("application %q, esperada %q", pino.Application, v.Application)
			}
			det, err := VerifySSHSIG(v.envelope(t), pino, v.Namespace, v.mensagem(t))
			if err != nil {
				t.Fatalf("o OpenSSH aceitou este vector e nos recusamo-lo: %v", err)
			}
			if det.Flags != v.Flags || det.Counter != v.Counter || det.HashAlgorithm != v.HashAlgorithm {
				t.Fatalf("detalhe %+v nao bate com o vector (flags=%d counter=%d hash=%s)", det, v.Flags, v.Counter, v.HashAlgorithm)
			}
		})
	}
}

// TestAOS446SSHSIGExigePresencaDeUtilizador — a única divergência DELIBERADA face ao OpenSSH.
//
// O vector `sem_toque` tem `flags = 0x00` e o `ssh-keygen -Y verify` do OpenSSH_10.3p1 diz
// «Good» (registado no próprio vector). Nós recusamos: um mandato existe para provar que um
// humano tocou na chave, e uma assinatura sem esse bit não prova nada que a seed em ficheiro que
// isto veio substituir já não provasse.
func TestAOS446SSHSIGExigePresencaDeUtilizador(t *testing.T) {
	v := carregarVectores(t)["sem_toque"]
	if v.Flags != 0 {
		t.Fatalf("o vector `sem_toque` tem de ter flags=0, tem %d", v.Flags)
	}
	if !strings.Contains(v.OpenSSHVeredicto, "Good") {
		t.Fatalf("o vector `sem_toque` so tem valor se o OpenSSH o ACEITAR; veredicto registado: %q", v.OpenSSHVeredicto)
	}
	_, err := VerifySSHSIG(v.envelope(t), v.pino(t), v.Namespace, v.mensagem(t))
	if err == nil {
		t.Fatal("uma assinatura sem presenca de utilizador foi ACEITE — e o unico ponto em que somos mais estritos do que o OpenSSH")
	}
	if !strings.Contains(err.Error(), "PRESENCA DE UTILIZADOR") {
		t.Fatalf("recusada pela razao errada: %v", err)
	}
}

// TestAOS446SSHSIGRecusaMutacoes — uma mutação por caso, cada uma sobre o vector que o OpenSSH
// aceitou. É o controlo negativo: sem ele, um verificador que devolvesse sempre nil passava o
// golden.
func TestAOS446SSHSIGRecusaMutacoes(t *testing.T) {
	vs := carregarVectores(t)
	bom := vs["sha512_up"]
	outra := vs["outra_application"]
	pino := bom.pino(t)
	msg := bom.mensagem(t)

	mutarUltimoByte := func(b []byte) []byte {
		c := append([]byte(nil), b...)
		c[len(c)-1] ^= 0x01
		return c
	}

	casos := []struct {
		nome     string
		envelope []byte
		pino     SKPublicKey
		ns       string
		msg      []byte
		contem   string
	}{
		{"contador mutado", mutarUltimoByte(bom.envelope(t)), pino, bom.Namespace, msg, "nao verifica"},
		{"mensagem trocada", bom.envelope(t), pino, bom.Namespace, []byte("outra mensagem"), "nao verifica"},
		{"namespace exigida diferente", bom.envelope(t), pino, "aos.identity.outra", msg, "namespace"},
		{"pino de outra chave", bom.envelope(t), outra.pino(t), bom.Namespace, msg, "o envelope traz a chave"},
		{"envelope truncado", bom.envelope(t)[:20], pino, bom.Namespace, msg, ""},
		{"preambulo trocado", append([]byte("SSHXIG"), bom.envelope(t)[6:]...), pino, bom.Namespace, msg, "preambulo"},
		{"envelope vazio", nil, pino, bom.Namespace, msg, "preambulo"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, err := VerifySSHSIG(c.envelope, c.pino, c.ns, c.msg); err == nil {
				t.Fatal("ACEITE — a mutacao devia avermelhar")
			} else if c.contem != "" && !strings.Contains(err.Error(), c.contem) {
				t.Fatalf("recusada pela razao errada: %v", err)
			}
		})
	}
}

// TestAOS446SSHSIGRecusaChaveDeSoftware — `ssh-ed25519` é um algoritmo SSHSIG perfeitamente
// legítimo, e é recusado de propósito: o caminho FIDO2 existe para exigir hardware, e aceitar uma
// chave de software por ele faria o `fmt` do mandato mentir.
func TestAOS446SSHSIGRecusaChaveDeSoftware(t *testing.T) {
	for _, linha := range []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNh",
	} {
		if _, err := ParseSKPublicKey(linha); err == nil {
			t.Fatalf("%q foi ACEITE como chave FIDO2 ed25519", strings.Fields(linha)[0])
		}
	}
}

// TestAOS446SSHSIGArmorIdaEVolta — o armor que o `ssh-keygen -Y sign` produz e o que nós
// produzimos são o mesmo texto, e o decodificador aguenta o BOM do PowerShell.
func TestAOS446SSHSIGArmorIdaEVolta(t *testing.T) {
	v := carregarVectores(t)["sha512_up"]
	env := v.envelope(t)
	armado := EncodeSSHSIGArmor(env)
	if !strings.HasPrefix(armado, "-----BEGIN SSH SIGNATURE-----\n") || !strings.HasSuffix(armado, "-----END SSH SIGNATURE-----\n") {
		t.Fatalf("armor mal formado:\n%s", armado)
	}
	// O base64 CRU deixou de ser aceite (achado A5 da revisão adversarial) — ver
	// TestAOS446ArmorRecusaOQueNaoSejaUmSoBloco, que cobre esse caso e mais seis.
	for _, entrada := range []string{armado, bomUTF8Rune + armado, "  \n" + armado + "\n  "} {
		volta, err := DecodeSSHSIGArmor(entrada)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if string(volta) != string(env) {
			t.Fatal("a volta nao devolve os mesmos bytes")
		}
	}
	for _, mau := range []string{"", "-----BEGIN SSH SIGNATURE-----\nAAAA\n", "AAAA\n-----END SSH SIGNATURE-----\n", "nao e base64 $$$"} {
		if _, err := DecodeSSHSIGArmor(mau); err == nil {
			t.Fatalf("%q foi aceite", mau)
		}
	}
}
