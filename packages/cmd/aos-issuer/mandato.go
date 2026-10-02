package main

// mandato.go — a cunhagem SEM OPERADOR (AOS-427, ADR-033).
//
// Dois subcomandos, e cada um corre num sítio diferente, de propósito:
//
//	mandate-sign   corre na máquina do HUMANO, com a chave DELE. Uma vez por mandato.
//	mint-mandated  corre no SERVIDOR, por timer, com a chave do emissor no Vault transit.
//
// O `mint-mandated` não tem nenhuma flag de identidade: humano, board, agente, classe, política e
// escopo vêm TODOS do mandato. Uma flag ao lado seria uma segunda fonte, e uma segunda fonte é o
// sítio por onde um timer mal configurado — ou um atacante com shell no servidor — escolheria o
// que o humano não assinou. O nó recusaria na mesma, mas recusar no emissor dá a causa ao
// operador em vez de a esconder atrás de um 403.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	identity "github.com/aos-ref/platform/identity"
)

// cmdMandateSign assina um mandato com a chave do humano e imprime-o em JSON (o ficheiro que se
// entrega ao servidor). O ID e a chave de revogação vão para `diag`: são o que o humano precisa de
// guardar para o poder revogar.
func cmdMandateSign(args []string, out, diag io.Writer) error {
	fs := flag.NewFlagSet("mandate-sign", flag.ContinueOnError)
	keyFile := fs.String("key-file", "", "seed ed25519 do HUMANO em hex (tem de existir: nunca se cria uma chave humana em silêncio)")
	human := fs.String("human", "", "user_id do humano (sem o prefixo human:) — o nome sob o qual a pubkey fica pinada no nó")
	board := fs.String("board", "", "board de soberania das NHIs cunhadas")
	agent := fs.String("agent", "", "id do agente que o emissor pode cunhar")
	class := fs.String("class", "", "classe do agente")
	policyRef := fs.String("policy-ref", "", "policy_ref (vazio ⇒ policy://<classe>, o que o mint manual usa)")
	caps := fs.String("caps", "", "tecto do escopo, CSV")
	issuer := fs.String("issuer", "iss:aos-issuer-auto", "o único emissor autorizado a cunhar sob este mandato")
	maxTTL := fs.Duration("max-ttl", 45*time.Minute, "TTL máximo de cada token cunhado (≤ 1h, o tecto da biblioteca)")
	validFor := fs.Duration("valid-for", 30*24*time.Hour, "validade do mandato a partir de agora (≤ 90 dias)")
	outFile := fs.String("out", "", "grava o mandato neste ficheiro (0600) em vez de o imprimir")
	requesters := fs.String("requesters", "", "OBRIGATÓRIO (AOS-439): os submissores por quem o emissor pode agir, CSV — o `sub` do ID-token de cada um, sem curingas; um service account só submete se estiver aqui")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" {
		return errors.New("mandate-sign exige --key-file (a chave do humano)")
	}
	// Verificado ANTES de ler a chave do humano: um mandato sem requesters seria recusado pela
	// biblioteca na mesma, mas não há razão para abrir a seed para o descobrir.
	if len(splitCSV(*requesters)) == 0 {
		return errors.New("mandate-sign exige --requesters (AOS-439): o mandato nomeia por quem o emissor pode agir — o sub de cada submissor, separados por virgula")
	}
	priv, err := lerSeedHumana(*keyFile)
	if err != nil {
		return err
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
	sm, err := identity.SignMandate(priv, identity.Mandate{
		ID: id, Human: *human, Board: *board, AgentID: *agent, AgentClass: *class, PolicyRef: pr,
		Scope: splitCSV(*caps), Issuer: *issuer, MaxTTLSeconds: int64(maxTTL.Seconds()),
		NotBefore: agora.Unix(), NotAfter: agora.Add(*validFor).Unix(),
		Requesters: splitCSV(*requesters),
	})
	if err != nil {
		return fmt.Errorf("mandate-sign: %w", err)
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
	fmt.Fprintf(diag, "mandato %s de %s, valido ate %s\n", id, *human, time.Unix(sm.Mandate.NotAfter, 0).UTC().Format(time.RFC3339))
	fmt.Fprintf(diag, "para o revogar: aos-issuer revoke-sign --jti %s ...  (POST /nhi/revoke)\n", identity.MandateRevocationKey(id))
	return nil
}

// cmdMintMandated cunha UM token sob o mandato, depois de o verificar contra a chave PINADA do
// humano — a mesma verificação que o nó fará. Com --out, escreve o token de forma ATÓMICA
// (ficheiro temporário + rename no mesmo directório): o `aos-orq consume` relê o ficheiro a cada
// submissão, e nunca pode ler meio token.
func cmdMintMandated(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mint-mandated", flag.ContinueOnError)
	keyFile := fs.String("key-file", "issuer-auto.key", "chave do emissor em ficheiro (só sem --vault-addr — em produção a chave vive no Vault)")
	buildSigner := vaultSignerFlags(fs, keyFile)
	mandateFile := fs.String("mandate", "", "ficheiro do mandato assinado (de mandate-sign)")
	signerPub := fs.String("signer-pubkey", "", "pino do humano — a MESMA entrada que está pinada no nó (AOS_MANDATE_SIGNERS): 64 hex, ou `sk-ssh-ed25519@openssh.com AAAA…` para uma chave FIDO2")
	// AOS-437: o timer do servidor passa a lista INTEIRA, no formato do nó, e a chave escolhe-se pelo
	// humano que o mandato nomeia. Uma só fonte para a chave pinada: o `.env` que o nó também lê — uma
	// segunda variável só com a pubkey seria uma cópia que se desactualiza sem ninguém ver.
	signers := fs.String("signers", "", "alternativa a --signer-pubkey: a lista AOS_MANDATE_SIGNERS do nó (user_id=hexpubkey,...)")
	ttl := fs.Duration("ttl", 0, "TTL do token (0 ⇒ o máximo do mandato)")
	caps := fs.String("caps", "", "subconjunto do escopo do mandato, CSV (vazio ⇒ o escopo inteiro)")
	outFile := fs.String("out", "", "escreve o token neste ficheiro, atomicamente, em vez de o imprimir")
	outMode := fs.String("out-mode", "0600", "permissões octais do ficheiro de --out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mandateFile == "" || (*signerPub == "") == (*signers == "") {
		return errors.New("mint-mandated exige --mandate e exactamente um de --signer-pubkey / --signers")
	}
	raw, err := os.ReadFile(*mandateFile)
	if err != nil {
		return fmt.Errorf("ler o mandato: %w", err)
	}
	var sm identity.SignedMandate
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sm); err != nil {
		return fmt.Errorf("mandato ilegivel: %w", err)
	}
	pinos, err := pinosDoHumano(strings.TrimSpace(*signerPub), *signers, sm.Mandate.Human)
	if err != nil {
		return err
	}
	// VERIFICA ANTES DE PEDIR UMA ASSINATURA AO VAULT: um mandato que não verifica não chega a
	// gerar tráfego para a chave do emissor.
	pino, err := verificarComAlgumPino(sm, pinos)
	if err != nil {
		return fmt.Errorf("mint-mandated: %w", err)
	}
	// Durante a janela de rotação diz-se QUAL pino serviu, no journal do timer: é por aqui que o
	// operador confirma que o mandato em uso já é o de hardware antes de remover o pino antigo.
	if len(pinos) > 1 {
		fmt.Fprintf(os.Stderr, "nota: %d pinos em vigor para %q (janela de rotacao); este mandato verificou com %s\n",
			len(pinos), sm.Mandate.Human, pino.Fingerprint())
	}
	m := sm.Mandate
	// AOS-439: um mandato v1 (sem requesters) ainda cunha — a decisão é do NÓ, pela janela de
	// migração (AOS_MANDATE_V1_UNTIL). Diz-se aqui, no journal do timer, para o operador ver que o
	// humano tem de re-assinar antes de a janela fechar.
	if m.Version() < 2 {
		fmt.Fprintf(os.Stderr, "aviso: mandato %s e v1 (sem requesters) — o no so o aceita dentro da janela AOS_MANDATE_V1_UNTIL; re-assinar com mandate-sign --requesters\n", m.ID)
	}
	d := *ttl
	if d == 0 {
		d = time.Duration(m.MaxTTLSeconds) * time.Second
	}
	scope := m.Scope
	if strings.TrimSpace(*caps) != "" {
		scope = splitCSV(*caps)
	}
	modo, err := parseModo(*outMode)
	if err != nil {
		return err
	}
	signer, err := buildSigner()
	if err != nil {
		return err
	}
	iss, err := identity.NewIssuerWithSigner(m.Issuer, signer, map[string]identity.ClassPolicy{
		m.AgentClass: {TTL: d, Scope: scope},
	})
	if err != nil {
		return fmt.Errorf("construir o emissor: %w", err)
	}
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: m.Human, AgentID: m.AgentID, AgentClass: m.AgentClass, PolicyRef: m.PolicyRef,
		Board: m.Board, UserAuthority: scope, AuthMethod: "mandate:" + m.ID, Mandate: &sm,
	})
	if err != nil {
		return fmt.Errorf("mint-mandated: %w", err)
	}
	if *outFile == "" {
		_, err = fmt.Fprintln(out, tok.Compact)
		return err
	}
	return escreverAtomico(*outFile, []byte(tok.Compact+"\n"), modo)
}

// pinoDoHumano resolve o PINO com que o mandato se verifica: um pino avulso (`--signer-pubkey`)
// ou o do humano que o mandato nomeia, tirado da lista no formato de `AOS_MANDATE_SIGNERS`
// (`--signers`).
//
// A GRAMÁTICA É A DO NÓ, E AGORA É LITERALMENTE A MESMA FUNÇÃO (AOS-446 fase 1). A versão
// anterior desta função — `pubkeyDoHumano` — percorria a lista à mão e devolvia TEXTO: aceitava
// nomes repetidos com chaves diferentes desde que o humano procurado só aparecesse uma vez, não
// via uma chave partilhada por dois nomes, e não teria como ler um pino FIDO2. Passa por
// [identity.ParseMandateSigners], que impõe a forma INTEIRA — e assim um `.env` que o nó recusa
// deixa de poder cunhar aqui, que era a divergência silenciosa entre os dois leitores.
// DEVOLVE TODOS OS PINOS DO HUMANO, e não um (AOS-446 fase 1, revisão de 2026-09-27): durante a
// janela de rotação o humano tem dois, e o emissor tem de aceitar o mandato que verifique com
// QUALQUER um deles — exactamente como o nó. Escolher um aqui faria o emissor recusar, com uma
// mensagem de assinatura inválida, o mandato que o nó aceitaria.
func pinosDoHumano(avulso, lista, humano string) ([]identity.MandateSigner, error) {
	if avulso != "" {
		s, err := identity.ParseMandateSigner(avulso)
		if err != nil {
			return nil, fmt.Errorf("--signer-pubkey: %w", err)
		}
		return []identity.MandateSigner{s}, nil
	}
	todos, err := identity.ParseMandateSigners(lista)
	if err != nil {
		return nil, fmt.Errorf("--signers: %w", err)
	}
	ps, ok := todos[humano]
	if !ok || len(ps) == 0 {
		return nil, fmt.Errorf("--signers nao tem chave pinada para o humano %q que o mandato nomeia", humano)
	}
	return ps, nil
}

// verificarComAlgumPino corre a verificação contra cada pino e devolve o que serviu. É a mesma
// regra do nó ([Verifier.verifyMandate]): um mandato verifica com um pino ou com nenhum.
func verificarComAlgumPino(sm identity.SignedMandate, pinos []identity.MandateSigner) (identity.MandateSigner, error) {
	var erros []string
	for _, p := range pinos {
		if err := sm.VerifySignature(p); err != nil {
			erros = append(erros, p.Fingerprint()+": "+err.Error())
			continue
		}
		return p, nil
	}
	// EMBRULHA A SENTINELA. «Nenhum pino verifica» É [identity.ErrMandateInvalid] — é o mesmo
	// veredicto que o nó dá, e quem chama compara com `errors.Is`. Um agregado sem `%w` perdia-a
	// e transformava um erro classificado num texto.
	return identity.MandateSigner{}, fmt.Errorf("%w: nenhum dos %d pino(s) de %q verifica o mandato — %s",
		identity.ErrMandateInvalid, len(pinos), sm.Mandate.Human, strings.Join(erros, " | "))
}

// lerSeedHumana lê a seed do humano SEM a criar se faltar — ao contrário de [loadOrCreateKey],
// que é a via de dev do emissor. Criar uma chave humana em silêncio assinaria um mandato com uma
// chave que não está pinada em lado nenhum.
func lerSeedHumana(caminho string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(caminho)
	if err != nil {
		return nil, fmt.Errorf("chave do humano %q: %w", caminho, err)
	}
	texto, err := limparSeedHex(raw)
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(texto)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("chave do humano %q invalida (esperado seed ed25519 de %d bytes em hex)", caminho, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func parseModo(s string) (os.FileMode, error) {
	var m uint32
	if _, err := fmt.Sscanf(s, "%o", &m); err != nil || m > 0o777 {
		return 0, fmt.Errorf("--out-mode %q invalido (octal, ex.: 0640)", s)
	}
	return os.FileMode(m), nil
}

// escreverAtomico escreve num temporário do MESMO directório e faz rename — atómico no mesmo
// sistema de ficheiros. Um leitor vê o token antigo ou o novo, nunca um meio-termo.
func escreverAtomico(caminho string, dados []byte, modo os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(caminho), ".nhi-*")
	if err != nil {
		return err
	}
	nome := tmp.Name()
	falhou := true
	defer func() {
		if falhou {
			_ = os.Remove(nome)
		}
	}()
	if _, err := tmp.Write(dados); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(nome, modo); err != nil {
		return err
	}
	if err := os.Rename(nome, caminho); err != nil {
		return err
	}
	falhou = false
	return nil
}
