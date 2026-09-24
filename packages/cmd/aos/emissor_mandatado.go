package main

// emissor_mandatado.go — o segundo trust anchor do nó: o emissor AUTOMÁTICO (AOS-427, ADR-033).
//
// O nó confiava num só emissor, e confiava por inteiro: o que a chave de AOS_ISSUER_ID assinasse,
// verificava. Isso está certo para o emissor manual, cuja chave vive na máquina do operador e
// cunha depois de dois logins. Não está certo para um emissor que cunha por timer num servidor
// cujo Vault se destrava sozinho — quem comprometesse o servidor cunharia o que quisesse.
//
// Por isso o emissor automático entra como um anchor SEPARADO, que o [identity.Verifier] só
// aceita DENTRO de um mandato assinado por um humano cuja chave está pinada AQUI, no nó. As três
// variáveis vêm juntas ou não vêm nenhuma, e as colisões que anulariam o limite abortam o arranque.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrBadMandatedIssuer — configuração do emissor mandatado incompleta, malformada, ou numa
// colisão que anularia o mandato. Aborta o arranque: um nó que anunciasse o limite sem o impor
// seria pior do que um nó sem emissor automático.
var ErrBadMandatedIssuer = errors.New("aos: emissor mandatado (AOS_MANDATED_ISSUER_ID/AOS_MANDATED_ISSUER_PUBKEY/AOS_MANDATE_SIGNERS) invalido")

// parseMandatedIssuer interpreta as três variáveis. Todas vazias ⇒ não configurado (zero, nil).
// Uma presente sem as outras ⇒ [ErrBadMandatedIssuer], a nomear as que faltam.
func parseMandatedIssuer(id, pubHex, signers string) (string, ed25519.PublicKey, map[string]ed25519.PublicKey, error) {
	id, pubHex, signers = strings.TrimSpace(id), strings.TrimSpace(pubHex), strings.TrimSpace(signers)
	if id == "" && pubHex == "" && signers == "" {
		return "", nil, nil, nil
	}
	var falta []string
	for _, c := range []struct{ nome, v string }{
		{"AOS_MANDATED_ISSUER_ID", id}, {"AOS_MANDATED_ISSUER_PUBKEY", pubHex}, {"AOS_MANDATE_SIGNERS", signers},
	} {
		if c.v == "" {
			falta = append(falta, c.nome)
		}
	}
	if len(falta) > 0 {
		return "", nil, nil, fmt.Errorf("%w: as tres variaveis vem juntas, faltam %s", ErrBadMandatedIssuer, strings.Join(falta, ", "))
	}
	pub, err := parseEd25519PubHex(pubHex)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: AOS_MANDATED_ISSUER_PUBKEY nao e uma pubkey ed25519 (64 hex)", ErrBadMandatedIssuer)
	}
	humanos, err := parseMandateSigners(signers)
	if err != nil {
		return "", nil, nil, err
	}
	return id, pub, humanos, nil
}

// parseMandateSigners interpreta AOS_MANDATE_SIGNERS: `user_id=hexpubkey,...`. O user_id é o do
// token, SEM o prefixo `human:`. Nome ou chave repetidos abortam — pelas razões de
// [parseOperators]: um nome com duas chaves é um conflito de autoridade, e uma chave com dois
// nomes destrói a atribuição (um mandato de bob seria aceite como sendo de alice).
func parseMandateSigners(s string) (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey)
	visto := make(map[string]string)
	for _, par := range strings.Split(s, ",") {
		if strings.TrimSpace(par) == "" {
			continue
		}
		kv := strings.SplitN(par, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS: entrada %q sem '=' (esperado user_id=hexpubkey)", ErrBadMandatedIssuer, strings.TrimSpace(par))
		}
		humano, raw := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if humano == "" || strings.HasPrefix(humano, "human:") {
			return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS: user_id %q vazio ou com o prefixo human: (usa o user_id do token)", ErrBadMandatedIssuer, humano)
		}
		if _, dup := out[humano]; dup {
			return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS: humano %q repetido", ErrBadMandatedIssuer, humano)
		}
		pub, err := parseEd25519PubHex(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS: pubkey de %q invalida (64 hex)", ErrBadMandatedIssuer, humano)
		}
		fp := hex.EncodeToString(pub)
		if outro, dup := visto[fp]; dup {
			return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS: %q e %q partilham a mesma pubkey", ErrBadMandatedIssuer, outro, humano)
		}
		visto[fp] = humano
		out[humano] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS sem nenhuma entrada valida", ErrBadMandatedIssuer)
	}
	return out, nil
}

// validarEmissorMandatado corre no arranque sobre a Config COMPOSTA (a Config também se constrói
// sem o ambiente, nos testes e no composition-root), e recusa as colisões que anulam o mandato.
func validarEmissorMandatado(cfg Config) error {
	if cfg.MandatedIssuerID == "" && len(cfg.MandatedIssuerPubKey) == 0 && len(cfg.MandateSigners) == 0 {
		return nil
	}
	if cfg.MandatedIssuerID == "" || len(cfg.MandatedIssuerPubKey) != ed25519.PublicKeySize || len(cfg.MandateSigners) == 0 {
		return fmt.Errorf("%w: id, pubkey de 32 bytes e pelo menos um humano pinado vem juntos", ErrBadMandatedIssuer)
	}
	// Cada chave pinada tem de ser uma pubkey: o verificador descartaria as outras em silêncio, e o
	// banner contaria humanos que não verificam nada.
	for humano, k := range cfg.MandateSigners {
		if humano == "" || len(k) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: humano %q sem pubkey ed25519 de 32 bytes", ErrBadMandatedIssuer, humano)
		}
	}
	// O MESMO NOME: o emissor manual passaria a exigir mandato, e os tokens dele deixariam de
	// verificar — ou, lido ao contrário, o automático herdaria o nome do que é confiado por inteiro.
	if cfg.MandatedIssuerID == cfg.IssuerID {
		return fmt.Errorf("%w: AOS_MANDATED_ISSUER_ID igual a AOS_ISSUER_ID (%q) — sao dois emissores, com dois nomes", ErrBadMandatedIssuer, cfg.IssuerID)
	}
	// A MESMA CHAVE: é a colisão que anula tudo. O automático assinaria com iss=AOS_ISSUER_ID e o
	// token verificaria contra o anchor manual — SEM mandato.
	if len(cfg.IssuerPubKey) > 0 && bytes.Equal(cfg.MandatedIssuerPubKey, cfg.IssuerPubKey) {
		return fmt.Errorf("%w: AOS_MANDATED_ISSUER_PUBKEY igual a AOS_ISSUER_PUBKEY — o emissor automatico cunharia como o manual, sem mandato", ErrBadMandatedIssuer)
	}
	// Um humano com a chave do emissor: quem cunha também assinaria os mandatos, e o limite seria
	// escrito por quem devia ser limitado.
	for humano, k := range cfg.MandateSigners {
		if bytes.Equal(k, cfg.MandatedIssuerPubKey) {
			return fmt.Errorf("%w: a chave pinada de %q e a do proprio emissor automatico — o limite seria escrito por quem devia limitar", ErrBadMandatedIssuer, humano)
		}
	}
	return nil
}

// emissorMandatadoPostureBanner declara, no arranque, se há emissor automático e o que o limita.
func emissorMandatadoPostureBanner(iss string, humanos int) []string {
	if iss == "" {
		return []string{
			"emissor MANDATADO (AOS-427, ADR-033): NAO COMPOSTO — so o emissor de AOS_ISSUER_ID e " +
				"confiado, e a cunhagem do NHI e MANUAL (a chave vive na maquina do operador). Para " +
				"cunhar sem operador: AOS_MANDATED_ISSUER_ID + AOS_MANDATED_ISSUER_PUBKEY + " +
				"AOS_MANDATE_SIGNERS, e um mandato assinado por um humano pinado (aos-issuer mandate-sign)",
		}
	}
	return []string{
		"emissor MANDATADO (AOS-427, ADR-033): COMPOSTO — os tokens de iss=" + strconv.Quote(iss) +
			" SO verificam com um MANDATO embebido, assinado por um dos " + strconv.Itoa(humanos) +
			" humano(s) pinado(s) em AOS_MANDATE_SIGNERS, e que cubra o token (humano, agente, classe, " +
			"politica, board, escopo, TTL e janela). Um EMISSOR comprometido (contentor, token do Vault, " +
			"chave transit) so cunha o que o humano assinou: fora do mandato e recusado AQUI " +
			"(E_MANDATE_VIOLATED), e um " +
			"mandato assinado por uma chave nao pinada tambem (E_MANDATE_INVALID). Revogar um mandato " +
			"mata todos os tokens cunhados sob ele: POST /nhi/revoke com jti=mandate:<id>. O emissor " +
			"manual (AOS_ISSUER_ID) continua confiado por inteiro. NAO cobre root no host deste no: esse " +
			"muda AOS_MANDATE_SIGNERS e reinicia",
	}
}
