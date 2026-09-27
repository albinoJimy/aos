package main

// mandato_fido2.go — ASSINAR UM MANDATO COM UMA CHAVE FIDO2 (AOS-446 fase 1, ADR-033 §6.4 e §8).
//
// # PORQUE SÃO DOIS COMANDOS E NÃO UM
//
// O `mandate-sign` lê uma seed ed25519 de um ficheiro e assina no processo. Com hardware isso é
// impossível aqui: assinar com um autenticador FIDO2 exige falar CTAP2/USB-HID, que é uma
// dependência (e um driver) que este binário não tem — e o ambiente de build é offline, portanto
// nem a poderia ganhar. O que existe, e o humano já tem instalado, é o `ssh-keygen -Y sign`.
//
// A cerimónia parte-se, então, em dois, e o passo do meio é o humano a tocar na chave:
//
//	1. aos-issuer mandate-prepare --human alice --board eu-west ... --out mandato
//	     escreve `mandato.mandate.json` (o documento) e `mandato.signing-input` (os bytes exactos
//	     a assinar) e imprime o comando do passo 2;
//
//	2. ssh-keygen -Y sign -f ~/.ssh/id_ed25519_sk -n aos.identity.mandate mandato.signing-input
//	     o autenticador pisca, o humano toca, e nasce `mandato.signing-input.sig`;
//
//	3. aos-issuer mandate-attach --mandate mandato.mandate.json --sig mandato.signing-input.sig \
//	       --signer "$(cat ~/.ssh/id_ed25519_sk.pub)" --out mandato.json
//	     verifica a assinatura contra o pino e emite o mandato assinado, com `fmt: sshsig`.
//
// O PASSO 3 VERIFICA ANTES DE EMITIR, e contra o pino que o operador diz ser o do nó. Um mandato
// que não verifica aqui também não verificaria no nó, e é melhor descobri-lo na máquina de quem
// assina do que num 403 três dias depois — o mesmo princípio do `mint-mandated`.
//
// # PORQUE O DOCUMENTO SE ESCREVE ANTES DE SER ASSINADO
//
// Porque tem um ID aleatório e uma janela temporal: não se consegue reconstruí-lo a partir das
// flags depois. O ficheiro do passo 1 NÃO é um mandato — é um rascunho sem assinatura, e o
// `mandate-attach` recusa-se a emitir se os bytes que o humano assinou não forem os dele.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	identity "github.com/aos-ref/platform/identity"
)

// sufixoDoDocumento e sufixoDosBytes são os nomes dos dois ficheiros do passo 1. O segundo é o
// que o `ssh-keygen` assina, e o `.sig` que ele produz junta-se-lhe por convenção dele.
const (
	sufixoDoDocumento = ".mandate.json"
	sufixoDosBytes    = ".signing-input"
)

// cmdMandatePrepare escreve o documento e os bytes a assinar. NÃO toca em chave nenhuma: é a
// diferença face ao `mandate-sign`, e é o que permite que a chave do humano nunca chegue a este
// processo.
func cmdMandatePrepare(args []string, out, diag io.Writer) error {
	fs := flag.NewFlagSet("mandate-prepare", flag.ContinueOnError)
	human := fs.String("human", "", "user_id do humano (sem o prefixo human:) — o nome sob o qual o pino fica no nó")
	board := fs.String("board", "", "board de soberania das NHIs cunhadas")
	agent := fs.String("agent", "", "id do agente que o emissor pode cunhar")
	class := fs.String("class", "", "classe do agente")
	policyRef := fs.String("policy-ref", "", "policy_ref (vazio ⇒ policy://<classe>)")
	caps := fs.String("caps", "", "tecto do escopo, CSV")
	issuer := fs.String("issuer", "iss:aos-issuer-auto", "o único emissor autorizado a cunhar sob este mandato")
	maxTTL := fs.Duration("max-ttl", 45*time.Minute, "TTL máximo de cada token cunhado (≤ 1h, o tecto da biblioteca)")
	validFor := fs.Duration("valid-for", 30*24*time.Hour, "validade do mandato a partir de agora (≤ 90 dias)")
	requesters := fs.String("requesters", "", "OBRIGATÓRIO (AOS-439): os submissores por quem o emissor pode agir, CSV")
	prefixo := fs.String("out", "", "prefixo dos dois ficheiros a escrever (<prefixo>"+sufixoDoDocumento+" e <prefixo>"+sufixoDosBytes+")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*prefixo) == "" {
		return errors.New("mandate-prepare exige --out (o prefixo dos dois ficheiros)")
	}
	if len(splitCSV(*requesters)) == 0 {
		return errors.New("mandate-prepare exige --requesters (AOS-439): o mandato nomeia por quem o emissor pode agir")
	}
	id, err := identity.NewMandateID()
	if err != nil {
		return err
	}
	pr := *policyRef
	if pr == "" {
		pr = "policy://" + *class
	}
	agora := time.Now().UTC()
	m := identity.Mandate{
		ID: id, Human: *human, Board: *board, AgentID: *agent, AgentClass: *class, PolicyRef: pr,
		Scope: splitCSV(*caps), Issuer: *issuer, MaxTTLSeconds: int64(maxTTL.Seconds()),
		NotBefore: agora.Unix(), NotAfter: agora.Add(*validFor).Unix(),
		Requesters: splitCSV(*requesters),
	}
	// VALIDA ANTES DE ESCREVER: um rascunho inválido só se descobriria depois de o humano ter
	// tocado na chave, e um toque desperdiçado é um toque que alguém repete sem olhar.
	if err := m.Validate(); err != nil {
		return fmt.Errorf("mandate-prepare: %w", err)
	}
	if m.Version() < 2 {
		return errors.New("mandate-prepare: requesters vazio — um mandato tem de nomear por quem o emissor pode agir")
	}
	doc, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	docPath, bytesPath := *prefixo+sufixoDoDocumento, *prefixo+sufixoDosBytes
	if err := os.WriteFile(docPath, append(doc, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(bytesPath, m.SigningInput(), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(diag, "mandato %s de %s, valido ate %s (RASCUNHO, ainda NAO assinado)\n",
		id, *human, time.Unix(m.NotAfter, 0).UTC().Format(time.RFC3339))
	fmt.Fprintf(diag, "1) assine com a chave FIDO2 (o autenticador vai pedir um toque):\n")
	fmt.Fprintf(diag, "     ssh-keygen -Y sign -f <chave_sk> -n %s %s\n", identity.MandateSSHSIGNamespace, bytesPath)
	fmt.Fprintf(diag, "2) junte a assinatura ao mandato:\n")
	fmt.Fprintf(diag, "     aos-issuer mandate-attach --mandate %s --sig %s.sig --signer \"$(cat <chave_sk>.pub)\" --out mandato.json\n", docPath, bytesPath)
	_, err = fmt.Fprintln(out, docPath)
	return err
}

// cmdMandateAttach junta a assinatura SSHSIG ao documento e emite o mandato assinado.
func cmdMandateAttach(args []string, out, diag io.Writer) error {
	fs := flag.NewFlagSet("mandate-attach", flag.ContinueOnError)
	docPath := fs.String("mandate", "", "ficheiro do documento produzido por mandate-prepare")
	sigPath := fs.String("sig", "", "ficheiro da assinatura SSHSIG (o .sig do ssh-keygen -Y sign)")
	pino := fs.String("signer", "", "o PINO do humano: a linha da chave publica `sk-ssh-ed25519@openssh.com AAAA…` — a MESMA que vai para AOS_MANDATE_SIGNERS")
	outFile := fs.String("out", "", "grava o mandato assinado neste ficheiro (0600) em vez de o imprimir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *docPath == "" || *sigPath == "" || strings.TrimSpace(*pino) == "" {
		return errors.New("mandate-attach exige --mandate, --sig e --signer")
	}
	signer, err := identity.ParseMandateSigner(strings.TrimSpace(*pino))
	if err != nil {
		return fmt.Errorf("--signer: %w", err)
	}
	if !signer.Hardware() {
		return fmt.Errorf("--signer e um pino de SOFTWARE (%s) — o mandate-attach existe para as chaves %s; para uma seed em ficheiro use `mandate-sign`",
			signer.Fingerprint(), identity.SSHSigAlgSKEd25519)
	}
	rawDoc, err := os.ReadFile(*docPath)
	if err != nil {
		return fmt.Errorf("ler o documento: %w", err)
	}
	var m identity.Mandate
	dec := json.NewDecoder(strings.NewReader(string(semBOM(rawDoc))))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf("documento ilegivel: %w", err)
	}
	rawSig, err := os.ReadFile(*sigPath)
	if err != nil {
		return fmt.Errorf("ler a assinatura: %w", err)
	}
	envelope, err := identity.DecodeSSHSIGArmor(string(rawSig))
	if err != nil {
		return fmt.Errorf("mandate-attach: %w", err)
	}
	sm, err := identity.AttachSSHSIG(m, envelope, signer)
	if err != nil {
		return fmt.Errorf("mandate-attach: %w", err)
	}
	corpo, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		return err
	}
	corpo = append(corpo, '\n')
	if *outFile != "" {
		if err := os.WriteFile(*outFile, corpo, 0o600); err != nil {
			return err
		}
	} else if _, err := out.Write(corpo); err != nil {
		return err
	}
	fmt.Fprintf(diag, "mandato %s de %s assinado em HARDWARE por %s, valido ate %s\n",
		sm.Mandate.ID, sm.Mandate.Human, signer.Fingerprint(), time.Unix(sm.Mandate.NotAfter, 0).UTC().Format(time.RFC3339))
	fmt.Fprintf(diag, "pine o humano no no com: AOS_MANDATE_SIGNERS=\"%s=%s\"\n", sm.Mandate.Human, strings.TrimSpace(*pino))
	fmt.Fprintf(diag, "para o revogar: aos-issuer revoke-sign --jti %s ...  (POST /nhi/revoke)\n", identity.MandateRevocationKey(sm.Mandate.ID))
	return nil
}
