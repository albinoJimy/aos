// Command aos-attest é a ferramenta de ASSINATURA e VERIFICAÇÃO da atestação de
// proveniência da imagem do nó `aos` (AOS-207 / ADR-017 ponto 3).
//
// PORQUÊ ESTA FERRAMENTA EXISTE (a decisão de AOS-207)
// ----------------------------------------------------
// O ponto 3 do ADR-017 exigia SBOM + atestação de proveniência ASSINADOS; a §Consequências
// admitia entregá-lo «na forma mínima (SBOM gerado, atestação POR ASSINAR)». Assinar exige
// uma primitiva de assinatura. Havia duas vias:
//
//	(A) FERRAMENTA EXTERNA (cosign/sigstore) no pipeline de entrega. É o padrão de facto:
//	    `cosign verify-attestation`, Rekor (log de transparência), certificados efémeros
//	    keyless e admission controllers (Kyverno, sigstore policy-controller) consomem-na
//	    sem tradução. CUSTO: introduz um binário externo na cadeia de ENTREGA, que teria de
//	    ser ele próprio pinado por digest e verificado; exige rede (Rekor) ou desligar o log
//	    de transparência; e NÃO é executável no ambiente deste repositório (build offline,
//	    `GOPROXY=off`, sem cosign no PATH). Nesse caminho o gate ficaria PERMANENTEMENTE
//	    saltado aqui — ou seja, DEF-06 fecharia com outro «declarado, não fingido» em vez de
//	    fechar com uma garantia imposta.
//
//	(B) ed25519 da STDLIB (`crypto/ed25519`), que é o que esta ferramenta faz. Zero
//	    dependência externa, corre offline, é testável no próprio repositório e torna a
//	    PROVA NEGATIVA executável aqui e agora (mexer no digest ⇒ gate vermelho).
//	    CUSTO — declarado, não escondido: nenhum verificador padrão consome a assinatura
//	    sem a nossa ferramenta; não há log de transparência (Rekor) nem certificados
//	    efémeros; a assinatura não fica ANEXADA à imagem no registry (OCI referrers), pelo
//	    que um admission controller de cluster continua sem material que consiga verificar.
//
// ESCOLHA: (B), com o custo (A) MITIGADO na forma do artefacto. O que se assina não é um
// formato caseiro: é um **envelope DSSE v1** (o mesmo que o cosign usa para atestações),
// com `payloadType = application/vnd.in-toto+json` e um **in-toto Statement v1** por dentro.
// Migrar para cosign no futuro é RE-EMBRULHAR os mesmos bytes de payload — não remodelar a
// atestação. O que a escolha (B) NÃO compra fica registado no ADR-017 §Consequências como
// residual NOMEADO (registry de imagens assinado, transparência, attestation de hardware).
//
// FRONTEIRA DA CARTA: este binário é um passo de ENTREGA (CI). NÃO entra na imagem do nó,
// NÃO é importado por `packages/**` e NÃO tem dependências externas — logo não consome a
// excepção escopada da emenda 1.3 da Carta (que é para o componente EXTERNO de autoridade
// de identidade). Zero-dep: só stdlib.
//
// CUSTÓDIA (ADR-017 ponto 5, agora com equivalente para a imagem): a chave PRIVADA de
// release NUNCA entra no repositório, em testes, em fixtures ou em variáveis de ambiente.
// Entra por CAMINHO de ficheiro (`-key`), montado read-only a partir do cofre — o mesmo
// padrão já documentado para `AOS_ISSUER_KEY_PATH` («transporta um caminho para material
// privado, não o material»). O `keygen` RECUSA escrever dentro de uma árvore git, para que a
// invariante «nenhuma chave privada no repositório» seja IMPOSTA e não só recomendada.
// O procedimento completo (quem assina, onde vive, como se roda) está em
// `deploy/node/CUSTODIA-CHAVE-RELEASE.md`.
//
// SUBCOMANDOS
//
//	aos-attest keygen  -out <seed> [-roster-entry <f>] [-holder <t>]  gera par ed25519 (fora do repo)
//	aos-attest pubkey  -key <seed>                                    imprime {keyid, publicKey}
//	aos-attest sign    -key <seed> -payload <stmt.json> -out <env>    envelope DSSE v1 assinado
//	aos-attest verify  -envelope <env> -roster <r> [-payload-out <f>] verifica assinatura+confiança
//
// O `verify` verifica APENAS a CRIPTOGRAFIA e a CONFIANÇA (assinatura válida, keyid conhecido,
// chave não revogada, dentro da janela de validade) e devolve o payload AUTENTICADO. A
// comparação dos digests contra a REALIDADE (imagem, binário, SBOM, manifesto) é do
// `scripts/ci/verify-attestation.sh` — separação deliberada: aqui não se decide o que é
// verdade sobre o artefacto, só quem o disse.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// payloadTypeInToto é o `payloadType` DSSE das atestações in-toto — o MESMO literal que o
// sigstore/cosign usa. É constante de propósito: aceitar um payloadType arbitrário
// permitiria assinar bytes e apresentá-los depois como se fossem uma atestação in-toto.
const payloadTypeInToto = "application/vnd.in-toto+json"

// algEd25519 é o único algoritmo suportado. Não há negociação: um campo `algorithm` que
// aceitasse outros valores seria uma superfície de downgrade sem implementação por trás.
const algEd25519 = "ed25519"

// rosterFormat / envelopeFormat — versões de formato explícitas (o mesmo hábito dos
// artefactos de `sbom.sh`): um consumidor futuro sabe contra que contrato está a ler.
const rosterFormat = "aos-release-pubkeys/v1"

// signature é uma assinatura DSSE (keyid + sig base64).
type signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// envelope é um envelope DSSE v1 (https://github.com/secure-systems-lab/dsse).
type envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"` // base64 STD do statement in-toto
	Signatures  []signature `json:"signatures"`
}

// rosterKey é uma chave PÚBLICA de release. Material público — vive no repositório.
type rosterKey struct {
	KeyID     string `json:"keyid"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"publicKey"` // hex, 64 chars (32 bytes)
	Holder    string `json:"holder"`
	Custody   string `json:"custody"`
	Status    string `json:"status"`             // active | rotated | revoked
	NotBefore string `json:"notBefore"`          // RFC3339 (vazio = sem limite inferior)
	NotAfter  string `json:"notAfter,omitempty"` // RFC3339 (vazio = sem limite superior)
}

// roster é o registo de chaves públicas de release confiáveis.
type roster struct {
	Format string      `json:"format"`
	Note   string      `json:"note"`
	Keys   []rosterKey `json:"keys"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = cmdKeygen(os.Args[2:])
	case "pubkey":
		err = cmdPubkey(os.Args[2:])
	case "sign":
		err = cmdSign(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "aos-attest: subcomando desconhecido %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "aos-attest: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `aos-attest — assinatura/verificação da atestação de entrega (AOS-207 / ADR-017 ponto 3)

  keygen  -out <seed> [-roster-entry <f>] [-holder <texto>]
          Gera um par ed25519. RECUSA escrever dentro de uma árvore git (a chave privada
          nunca entra no repositório). Escreve a seed com permissões 0600.
  pubkey  -key <seed>
          Imprime {keyid, publicKey, algorithm} em JSON.
  sign    -key <seed> -payload <statement.json> -out <envelope.json>
          Emite um envelope DSSE v1 (payloadType application/vnd.in-toto+json).
  verify  -envelope <envelope.json> -roster <pubkeys.json> [-payload-out <f>] [-now <RFC3339>]
          Verifica a assinatura contra o registo de chaves públicas e devolve o payload
          AUTENTICADO. NÃO compara digests com a realidade — isso é verify-attestation.sh.
`)
}

// ---------------------------------------------------------------------------
// Leitura/escrita de ficheiros. TODO o caminho vindo de flag passa por filepath.Clean e
// toda a escrita usa 0600: a ferramenta nunca cria material legível por terceiros.

func readFileClean(path string) ([]byte, error) {
	return os.ReadFile(filepath.Clean(path))
}

func writeFile0600(path string, data []byte) error {
	return os.WriteFile(filepath.Clean(path), data, 0o600)
}

// ---------------------------------------------------------------------------
// Chaves

// loadSeed lê uma seed ed25519 de 32 bytes em hex (64 chars) de um ficheiro.
//
// FORMATO ESTRITO de propósito: aceitar PEM/base64/variantes multiplicaria os caminhos por
// onde material privado pode ser mal interpretado. Comentários (linhas iniciadas por `#`)
// são tolerados para que o ficheiro do cofre possa trazer um cabeçalho de custódia.
func loadSeed(path string) (ed25519.PrivateKey, error) {
	raw, err := readFileClean(path)
	if err != nil {
		return nil, fmt.Errorf("ler o ficheiro da chave: %w", err)
	}
	var body strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		body.WriteString(line)
	}
	seedHex := strings.TrimSpace(body.String())
	if len(seedHex) != ed25519.SeedSize*2 {
		// NUNCA ecoar o conteúdo: a mensagem de erro fala de comprimento, não de material.
		return nil, fmt.Errorf("chave malformada: esperados %d chars hex (seed ed25519 de %d bytes), lidos %d",
			ed25519.SeedSize*2, ed25519.SeedSize, len(seedHex))
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, errors.New("chave malformada: conteúdo não é hexadecimal")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// keyIDOf é o identificador determinístico de uma chave pública: sha256(pubkey) em hex.
// Determinístico ⇒ o registo de chaves públicas e o envelope referem-se à MESMA chave sem
// depender de um nome atribuído à mão (que se pode reciclar em silêncio).
func keyIDOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// keygen

func cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "caminho do ficheiro de seed a criar (FORA de qualquer árvore git)")
	rosterEntry := fs.String("roster-entry", "", "caminho opcional onde escrever a entrada de registo (material PÚBLICO)")
	holder := fs.String("holder", "POR PREENCHER — nome/função do detentor", "detentor declarado da chave")
	custody := fs.String("custody", "POR PREENCHER — cofre/HSM onde a privada vive", "custódia declarada da chave")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("keygen: -out é obrigatório")
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		return fmt.Errorf("keygen: resolver -out: %w", err)
	}
	// INVARIANTE IMPOSTA (não recomendada): nenhuma chave privada dentro de uma árvore git.
	// Um `.gitignore` é uma promessa; isto é uma recusa.
	if repo := gitWorktreeOf(filepath.Dir(abs)); repo != "" {
		return fmt.Errorf("keygen RECUSADO: %s fica dentro da árvore git %s.\n"+
			"  A chave PRIVADA de release nunca entra num repositório (ADR-017 ponto 5 aplicado à imagem).\n"+
			"  Escolha um caminho fora do repositório (cofre, volume efémero, HSM)", abs, repo)
	}
	if _, err := os.Stat(abs); err == nil {
		return fmt.Errorf("keygen RECUSADO: %s já existe — não se sobrescreve material de chave em silêncio", abs)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("keygen: gerar par: %w", err)
	}
	seed := priv.Seed()
	header := "# seed ed25519 (32 bytes hex) da chave de assinatura de release do nó `aos` (AOS-207).\n" +
		"# MATERIAL PRIVADO. Nunca committar, nunca colocar em variável de ambiente, nunca na imagem.\n"
	if err := writeFile0600(abs, []byte(header+hex.EncodeToString(seed)+"\n")); err != nil {
		return fmt.Errorf("keygen: escrever a seed: %w", err)
	}
	kid := keyIDOf(pub)
	if *rosterEntry != "" {
		entry := rosterKey{
			KeyID:     kid,
			Algorithm: algEd25519,
			PublicKey: hex.EncodeToString(pub),
			Holder:    *holder,
			Custody:   *custody,
			Status:    "active",
			NotBefore: time.Now().UTC().Format(time.RFC3339),
		}
		r := roster{
			Format: rosterFormat,
			Note:   "Material PÚBLICO. Gerado por `aos-attest keygen`; a privada NÃO está aqui.",
			Keys:   []rosterKey{entry},
		}
		blob, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fmt.Errorf("keygen: serializar a entrada de registo: %w", err)
		}
		if err := writeFile0600(*rosterEntry, append(blob, '\n')); err != nil {
			return fmt.Errorf("keygen: escrever a entrada de registo: %w", err)
		}
	}
	// stdout traz SÓ material público.
	fmt.Printf("{\n  \"keyid\": %q,\n  \"publicKey\": %q,\n  \"algorithm\": %q,\n  \"seedFile\": %q\n}\n",
		kid, hex.EncodeToString(pub), algEd25519, abs)
	return nil
}

// gitWorktreeOf devolve a raiz da árvore git que contém `dir`, ou "" se não houver nenhuma.
// Sobe a hierarquia à procura de `.git` (directório OU ficheiro, para cobrir worktrees).
func gitWorktreeOf(dir string) string {
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// ---------------------------------------------------------------------------
// pubkey

func cmdPubkey(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ContinueOnError)
	key := fs.String("key", "", "ficheiro da seed ed25519")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" {
		return errors.New("pubkey: -key é obrigatório")
	}
	priv, err := loadSeed(*key)
	if err != nil {
		return err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("pubkey: chave pública de tipo inesperado")
	}
	fmt.Printf("{\n  \"keyid\": %q,\n  \"publicKey\": %q,\n  \"algorithm\": %q\n}\n",
		keyIDOf(pub), hex.EncodeToString(pub), algEd25519)
	return nil
}

// ---------------------------------------------------------------------------
// DSSE

// pae é o Pre-Authentication Encoding do DSSE v1:
//
//	PAE(t, b) = "DSSEv1" SP len(t) SP t SP len(b) SP b
//
// PORQUÊ IMPORTA: assina-se o PAE, não o payload cru. Sem ele, um payload podia ser
// re-apresentado sob outro `payloadType` (confusão de tipo) com a MESMA assinatura válida.
func pae(payloadType string, payload []byte) []byte {
	prefix := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	out := make([]byte, 0, len(prefix)+len(payload))
	out = append(out, prefix...)
	out = append(out, payload...)
	return out
}

func cmdSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	key := fs.String("key", "", "ficheiro da seed ed25519 (material privado, fora do repo)")
	payload := fs.String("payload", "", "ficheiro do in-toto Statement a assinar")
	out := fs.String("out", "", "ficheiro do envelope DSSE a escrever")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" || *payload == "" || *out == "" {
		return errors.New("sign: -key, -payload e -out são obrigatórios")
	}
	priv, err := loadSeed(*key)
	if err != nil {
		return err
	}
	body, err := readFileClean(*payload)
	if err != nil {
		return fmt.Errorf("sign: ler o payload: %w", err)
	}
	// O payload TEM de ser JSON válido: assinar bytes opacos e chamar-lhes atestação in-toto
	// seria produzir uma garantia que nenhum verificador consegue interpretar.
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		return fmt.Errorf("sign: o payload não é um objecto JSON válido: %w", err)
	}
	if probe["_type"] != "https://in-toto.io/Statement/v1" {
		return fmt.Errorf("sign: payload não é um in-toto Statement v1 (_type=%v)", probe["_type"])
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("sign: chave pública de tipo inesperado")
	}
	sig := ed25519.Sign(priv, pae(payloadTypeInToto, body))
	env := envelope{
		PayloadType: payloadTypeInToto,
		Payload:     base64.StdEncoding.EncodeToString(body),
		Signatures:  []signature{{KeyID: keyIDOf(pub), Sig: base64.StdEncoding.EncodeToString(sig)}},
	}
	blob, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("sign: serializar o envelope: %w", err)
	}
	if err := writeFile0600(*out, append(blob, '\n')); err != nil {
		return fmt.Errorf("sign: escrever o envelope: %w", err)
	}
	fmt.Printf("assinado: %s (keyid %s, %d bytes de payload)\n", *out, keyIDOf(pub), len(body))
	return nil
}

// ---------------------------------------------------------------------------
// verify

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	envPath := fs.String("envelope", "", "ficheiro do envelope DSSE")
	rosterPath := fs.String("roster", "", "registo de chaves públicas de release")
	payloadOut := fs.String("payload-out", "", "ficheiro onde escrever o payload AUTENTICADO")
	nowStr := fs.String("now", "", "instante de avaliação da janela de validade (RFC3339; default: agora)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envPath == "" || *rosterPath == "" {
		return errors.New("verify: -envelope e -roster são obrigatórios")
	}
	now := time.Now().UTC()
	if *nowStr != "" {
		t, err := time.Parse(time.RFC3339, *nowStr)
		if err != nil {
			return fmt.Errorf("verify: -now inválido (%q): %w", *nowStr, err)
		}
		now = t.UTC()
	}

	envBlob, err := readFileClean(*envPath)
	if err != nil {
		return fmt.Errorf("verify: ler o envelope: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(envBlob, &env); err != nil {
		return fmt.Errorf("verify: envelope malformado: %w", err)
	}
	if env.PayloadType != payloadTypeInToto {
		return fmt.Errorf("verify RECUSADO: payloadType %q != %q (confusão de tipo)", env.PayloadType, payloadTypeInToto)
	}
	if len(env.Signatures) == 0 {
		return errors.New("verify RECUSADO: envelope sem assinaturas")
	}
	body, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return fmt.Errorf("verify RECUSADO: payload não é base64 válido: %w", err)
	}

	rosterBlob, err := readFileClean(*rosterPath)
	if err != nil {
		return fmt.Errorf("verify: ler o registo de chaves: %w", err)
	}
	var r roster
	if err := json.Unmarshal(rosterBlob, &r); err != nil {
		return fmt.Errorf("verify: registo de chaves malformado: %w", err)
	}
	if r.Format != rosterFormat {
		return fmt.Errorf("verify RECUSADO: registo de chaves com formato %q (esperado %q)", r.Format, rosterFormat)
	}
	if len(r.Keys) == 0 {
		return errors.New("verify RECUSADO: registo de chaves VAZIO — não há chave de release provisionada.\n" +
			"  Isto é fail-closed por desenho: uma atestação assinada por uma chave que ninguém declara\n" +
			"  confiar não é uma garantia. Ver deploy/node/CUSTODIA-CHAVE-RELEASE.md")
	}
	byID := make(map[string]rosterKey, len(r.Keys))
	for _, k := range r.Keys {
		byID[k.KeyID] = k
	}

	// Uma chave REVOGADA que apareça no envelope invalida a verificação INTEIRA, mesmo que
	// outra assinatura seja válida: co-assinar com material revogado é sinal de compromisso,
	// não um detalhe a ignorar por haver quórum.
	for _, s := range env.Signatures {
		if k, ok := byID[s.KeyID]; ok && k.Status == "revoked" {
			return fmt.Errorf("verify RECUSADO: o envelope traz assinatura da chave REVOGADA %s", s.KeyID)
		}
	}

	msg := pae(env.PayloadType, body)
	var reasons []string
	for _, s := range env.Signatures {
		k, ok := byID[s.KeyID]
		if !ok {
			reasons = append(reasons, fmt.Sprintf("keyid %s: DESCONHECIDO no registo de chaves", s.KeyID))
			continue
		}
		if k.Algorithm != algEd25519 {
			reasons = append(reasons, fmt.Sprintf("keyid %s: algoritmo %q não suportado", s.KeyID, k.Algorithm))
			continue
		}
		if err := keyWindowOK(k, now); err != nil {
			reasons = append(reasons, fmt.Sprintf("keyid %s: %v", s.KeyID, err))
			continue
		}
		pubBytes, err := hex.DecodeString(k.PublicKey)
		if err != nil || len(pubBytes) != ed25519.PublicKeySize {
			reasons = append(reasons, fmt.Sprintf("keyid %s: chave pública malformada no registo", s.KeyID))
			continue
		}
		// COERÊNCIA keyid↔chave: sem isto, uma entrada podia declarar o keyid de uma chave
		// e o material de outra, e a confiança passaria a ser no rótulo, não na chave.
		if subtle.ConstantTimeCompare([]byte(keyIDOf(pubBytes)), []byte(k.KeyID)) != 1 {
			reasons = append(reasons, fmt.Sprintf("keyid %s: INCOERENTE — sha256(publicKey) não bate com o keyid declarado", s.KeyID))
			continue
		}
		sigBytes, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("keyid %s: assinatura não é base64 válido", s.KeyID))
			continue
		}
		if !ed25519.Verify(pubBytes, msg, sigBytes) {
			reasons = append(reasons, fmt.Sprintf("keyid %s: ASSINATURA INVÁLIDA sobre o PAE do payload", s.KeyID))
			continue
		}
		// Verde: assinatura válida de chave confiável e dentro da janela.
		if *payloadOut != "" {
			if err := writeFile0600(*payloadOut, body); err != nil {
				return fmt.Errorf("verify: escrever o payload autenticado: %w", err)
			}
		}
		fmt.Printf("assinatura VÁLIDA: keyid=%s detentor=%q estado=%s payload=%d bytes\n",
			k.KeyID, k.Holder, k.Status, len(body))
		return nil
	}
	return fmt.Errorf("verify RECUSADO: nenhuma assinatura confiável no envelope:\n  - %s", strings.Join(reasons, "\n  - "))
}

// keyWindowOK decide se a chave `k` é utilizável em `now`.
//
// `active`  — utilizável dentro da janela (se declarada).
// `rotated` — utilizável SÓ dentro da janela, e a janela TEM de ter fim: uma chave rodada
// sem `notAfter` seria uma chave activa com outro nome.
// `revoked` — nunca (já recusada antes de aqui chegar).
func keyWindowOK(k rosterKey, now time.Time) error {
	switch k.Status {
	case "active", "rotated":
	case "revoked":
		return errors.New("chave REVOGADA")
	default:
		return fmt.Errorf("estado %q fora do vocabulário (active|rotated|revoked)", k.Status)
	}
	if k.NotBefore != "" {
		nb, err := time.Parse(time.RFC3339, k.NotBefore)
		if err != nil {
			return fmt.Errorf("notBefore malformado (%q)", k.NotBefore)
		}
		if now.Before(nb) {
			return fmt.Errorf("fora da janela: agora (%s) é ANTES de notBefore (%s)", now.Format(time.RFC3339), k.NotBefore)
		}
	}
	if k.Status == "rotated" && k.NotAfter == "" {
		return errors.New("estado `rotated` sem `notAfter` — uma chave rodada sem fim de janela é uma chave activa com outro nome")
	}
	if k.NotAfter != "" {
		na, err := time.Parse(time.RFC3339, k.NotAfter)
		if err != nil {
			return fmt.Errorf("notAfter malformado (%q)", k.NotAfter)
		}
		if !now.Before(na) {
			return fmt.Errorf("fora da janela: agora (%s) é DEPOIS de notAfter (%s)", now.Format(time.RFC3339), k.NotAfter)
		}
	}
	return nil
}
