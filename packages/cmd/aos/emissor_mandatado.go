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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// ErrBadMandatedIssuer — configuração do emissor mandatado incompleta, malformada, ou numa
// colisão que anularia o mandato. Aborta o arranque: um nó que anunciasse o limite sem o impor
// seria pior do que um nó sem emissor automático.
var ErrBadMandatedIssuer = errors.New("aos: emissor mandatado (AOS_MANDATED_ISSUER_ID/AOS_MANDATED_ISSUER_PUBKEY/AOS_MANDATE_SIGNERS) invalido")

// parseMandatedIssuer interpreta as três variáveis. Todas vazias ⇒ não configurado (zero, nil).
// Uma presente sem as outras ⇒ [ErrBadMandatedIssuer], a nomear as que faltam.
func parseMandatedIssuer(id, pubHex, signers string) (string, ed25519.PublicKey, map[string][]identity.MandateSigner, error) {
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

// parseMandateSigners interpreta AOS_MANDATE_SIGNERS: `user_id=pino,...`, onde o pino é 64 hex
// (chave ed25519 de software) ou uma chave FIDO2 `sk-ssh-ed25519@openssh.com AAAA…` (AOS-446
// fase 1). O user_id é o do token, SEM o prefixo `human:`.
//
// A GRAMÁTICA VIVE NO PACOTE `identity`, E ESTA FUNÇÃO SÓ A EMBRULHA NO ERRO DO NÓ. Havia dois
// leitores da mesma lista — este e o `pubkeyDoHumano` do `aos-issuer` —, e enquanto o pino era
// sempre hex a divergência entre eles era invisível. Com duas formas de pino deixa de ser: o
// leitor que só conhecesse uma recusaria mandatos que o outro aceita. Quem pode assinar um
// mandato não pode ter duas definições; ver `platform/identity/mandatesigner.go`.
//
// As recusas que já eram do nó mantêm-se todas, agora impostas nos dois lados: nome repetido é um
// conflito de autoridade, e uma chave com dois nomes destrói a atribuição (um mandato de bob
// seria aceite como sendo de alice).
func parseMandateSigners(s string) (map[string][]identity.MandateSigner, error) {
	out, err := identity.ParseMandateSigners(s)
	if err != nil {
		return nil, fmt.Errorf("%w: AOS_MANDATE_SIGNERS: %v", ErrBadMandatedIssuer, err)
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
	// Cada pino tem de estar formado: o verificador descartaria os outros em silêncio, e o banner
	// contaria humanos que não verificam nada.
	for humano, ks := range cfg.MandateSigners {
		if humano == "" || len(ks) == 0 {
			return fmt.Errorf("%w: humano %q sem nenhum pino", ErrBadMandatedIssuer, humano)
		}
		if len(ks) > identity.MandatePinosMaximoPorHumano {
			return fmt.Errorf("%w: humano %q com %d pinos, acima do tecto de %d", ErrBadMandatedIssuer, humano, len(ks), identity.MandatePinosMaximoPorHumano)
		}
		for _, k := range ks {
			if !k.Valid() {
				return fmt.Errorf("%w: humano %q sem pino valido (pubkey ed25519 em hex, ou chave %s)", ErrBadMandatedIssuer, humano, identity.SSHSigAlgSKEd25519)
			}
		}
	}
	// A JANELA DE ROTAÇÃO TEM DE ESTAR ABERTA PARA HAVER DOIS PINOS (AOS-446 fase 1, revisão de
	// 2026-09-27). Aqui, no arranque, e não só na verificação: um nó que arranque com dois pinos
	// e a janela fechada recusaria TODOS os mandatos desse humano em silêncio, e o operador
	// descobria-o pela drenagem parada. Aborta e diz o que fazer.
	if dois := identity.HumanosComDoisPinos(cfg.MandateSigners); len(dois) > 0 {
		if cfg.MandateDualPinUntil.IsZero() {
			return fmt.Errorf("%w: %s tem mais de um pino e AOS_MANDATE_DUAL_PIN_UNTIL nao esta definida — a janela de rotacao esta FECHADA (defina-a, ou deixe um so pino por humano)",
				ErrBadMandatedIssuer, strings.Join(dois, ", "))
		}
		if !time.Now().UTC().Before(cfg.MandateDualPinUntil) {
			return fmt.Errorf("%w: %s tem mais de um pino e a janela de rotacao (AOS_MANDATE_DUAL_PIN_UNTIL=%s) ja FECHOU — remova o pino antigo",
				ErrBadMandatedIssuer, strings.Join(dois, ", "), cfg.MandateDualPinUntil.Format(time.RFC3339))
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
	// escrito por quem devia ser limitado. Um pino FIDO2 nunca pode colidir com a chave do emissor
	// (que é uma pubkey crua), e por isso a comparação só se aplica aos pinos de software — mas
	// faz-se sobre [identity.MandateSigner.RawKey], e não sobre um campo, para que um pino de
	// hardware não passe por aqui como «chave vazia igual a chave vazia».
	for humano, ks := range cfg.MandateSigners {
		for _, k := range ks {
			if raw := k.RawKey(); len(raw) > 0 && bytes.Equal(raw, cfg.MandatedIssuerPubKey) {
				return fmt.Errorf("%w: a chave pinada de %q e a do proprio emissor automatico — o limite seria escrito por quem devia limitar", ErrBadMandatedIssuer, humano)
			}
		}
	}
	return nil
}

// ErrBadMandateV1Until — `AOS_MANDATE_V1_UNTIL` malformada ou demasiado longe. Aborta o arranque.
var ErrBadMandateV1Until = errors.New("aos: AOS_MANDATE_V1_UNTIL invalida")

// parseMandateV1Until interpreta o fim da janela de migração dos mandatos v1 (AOS-439): um
// instante RFC 3339. Vazia ⇒ zero ⇒ janela FECHADA (um v1 é recusado).
//
// O TECTO: um fim mais longe do que [identity.MandatoValidadeMaxima] a contar do arranque aborta.
// Um mandato v1 nunca vive mais do que isso, pelo que uma janela maior só serviria para aceitar v1
// que ainda nem foram assinados — e um valor como `2099-01-01` é o que um operador escreve para
// «desligar a verificação». Não é uma defesa contra quem reescreve o `.env`: é um tecto contra o
// esquecimento, como o próprio tecto do mandato.
func parseMandateV1Until(s string, agora time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q nao e RFC 3339 (ex.: 2026-10-25T23:59:59Z)", ErrBadMandateV1Until, s)
	}
	if t.After(agora.Add(identity.MandatoValidadeMaxima)) {
		return time.Time{}, fmt.Errorf("%w: %s esta a mais de %s do arranque — a janela serve a migracao, nao a desliga",
			ErrBadMandateV1Until, t.UTC().Format(time.RFC3339), identity.MandatoValidadeMaxima)
	}
	return t.UTC(), nil
}

// janelaV1PostureBanner declara a janela de migração dos mandatos v1, pelo relógio do arranque.
// Só é emitida com o emissor mandatado composto — sem ele não há mandatos a verificar.
func janelaV1PostureBanner(iss string, ate, agora time.Time) []string {
	if iss == "" {
		return nil
	}
	switch {
	case ate.IsZero():
		return []string{"mandatos v1 (sem requesters, AOS-439): RECUSADOS — AOS_MANDATE_V1_UNTIL vazia, a janela " +
			"de migracao esta FECHADA. So um mandato v2 (mandate-sign --requesters) cunha tokens aceites, e o POST " +
			"/runs sob ele exige que o submissor derivado pelo no conste dos requesters"}
	case !agora.Before(ate):
		return []string{"mandatos v1 (sem requesters, AOS-439): RECUSADOS — a janela de migracao fechou em " +
			ate.UTC().Format(time.RFC3339) + ". So um mandato v2 (mandate-sign --requesters) cunha tokens aceites"}
	default:
		return []string{"mandatos v1 (sem requesters, AOS-439): ACEITES ate " + ate.UTC().Format(time.RFC3339) +
			" (janela de migracao, AOS_MANDATE_V1_UNTIL) — sob um v1 o emissor age por QUALQUER submissor; o " +
			"humano tem de re-assinar com mandate-sign --requesters antes do fim, ou a drenagem para. Um v2 exige " +
			"que o submissor derivado pelo no conste dos requesters"}
	}
}

// pinosDeHardware conta quantos PINOS (não humanos) são chaves FIDO2, e quantos são ao todo.
// Conta pinos e não humanos desde a janela de rotação: durante ela um humano tem os dois, e
// dizer «1 de 1 humano em hardware» esconderia que a chave de software ainda assina.
func pinosDeHardware(signers map[string][]identity.MandateSigner) (hw, total int) {
	for _, ks := range signers {
		for _, s := range ks {
			total++
			if s.Hardware() {
				hw++
			}
		}
	}
	return hw, total
}

// ErrBadMandateDualPinUntil — `AOS_MANDATE_DUAL_PIN_UNTIL` malformada ou demasiado longe.
var ErrBadMandateDualPinUntil = errors.New("aos: AOS_MANDATE_DUAL_PIN_UNTIL invalida")

// parseMandateDualPinUntil interpreta o fim da JANELA DE ROTAÇÃO de pinos (AOS-446 fase 1): um
// instante RFC 3339. Vazia ⇒ zero ⇒ janela FECHADA (um humano só pode ter um pino).
//
// O TECTO é o mesmo da janela dos v1 ([identity.MandatoValidadeMaxima] a contar do arranque), e
// pela mesma razão: uma janela mais longa do que a vida de um mandato não serve uma rotação —
// serve para alguém se esquecer de a fechar, que é exactamente o estado em que duas chaves
// assinam mandatos sem ninguém saber porquê.
func parseMandateDualPinUntil(s string, agora time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q nao e RFC 3339 (ex.: 2026-10-10T23:59:59Z)", ErrBadMandateDualPinUntil, s)
	}
	if t.After(agora.Add(identity.MandatoValidadeMaxima)) {
		return time.Time{}, fmt.Errorf("%w: %s esta a mais de %s do arranque — a janela serve UMA rotacao, nao a desliga",
			ErrBadMandateDualPinUntil, t.UTC().Format(time.RFC3339), identity.MandatoValidadeMaxima)
	}
	return t.UTC(), nil
}

// janelaDeRotacaoPostureBanner declara a janela de rotação de pinos, pelo relógio do arranque.
//
// `epoca` é a versão que o WORM escreve e `v1Ate` o fim da janela dos mandatos v1: as duas entram
// porque o que torna a rotação SEGURA depende delas, e a linha tem de o dizer — ver os dois
// blocos de aviso abaixo (achados B2 e B4 da 2.ª ronda de revisão).
func janelaDeRotacaoPostureBanner(iss string, signers map[string][]identity.MandateSigner, ate, agora time.Time, epoca uint8, v1Ate time.Time) []string {
	if iss == "" {
		return nil
	}
	dois := identity.HumanosComDoisPinos(signers)
	if len(dois) == 0 {
		if ate.IsZero() {
			return []string{"rotacao de pinos (AOS-446 fase 1): UM pino por humano, janela FECHADA (AOS_MANDATE_DUAL_PIN_UNTIL vazia). " +
				"Trocar a chave de um humano invalida TODOS os mandatos dele no mesmo instante — para rodar sem parar a cunhagem, " +
				"defina a janela, acrescente o pino novo ao lado do antigo, entregue o mandato novo, e so depois remova o antigo"}
		}
		return []string{"rotacao de pinos (AOS-446 fase 1): janela ABERTA ate " + ate.UTC().Format(time.RFC3339) +
			", mas NENHUM humano tem dois pinos — nao ha rotacao a decorrer. Feche-a (apague AOS_MANDATE_DUAL_PIN_UNTIL) quando acabar"}
	}
	linhas := []string{"rotacao de pinos (AOS-446 fase 1): EM CURSO para " + strings.Join(dois, ", ") +
		" — DOIS pinos em vigor, os dois assinam mandatos aceites, ate " + ate.UTC().Format(time.RFC3339) +
		". O `mandate_signer` de cada decisao selada diz QUAL verificou: confirme pelo WORM que o mandato em uso ja e o novo, " +
		"remova o pino antigo de AOS_MANDATE_SIGNERS e apague AOS_MANDATE_DUAL_PIN_UNTIL. Passada a data, o arranque ABORTA com dois pinos " +
		"e um mandato desse humano e recusado (E_MANDATE_DUAL_PIN_CLOSED)"}

	// B2 — A ÉPOCA DE ESCRITA DECIDE SE A ROTAÇÃO É CONFERÍVEL, E TEM DE SE DIZER.
	//
	// O passo 7 do procedimento manda confirmar, no `audit-trail`, QUAL pino verificou o mandato
	// em uso — é o que separa fechar a janela com prova de a fechar às cegas. Só que o
	// `mandate_signer` só entra no selo a partir do v5: abaixo disso o `stampSchema` APAGA-O, e o
	// operador faz o grep, não vê nada, e conclui o que lhe apetecer. Produção escreve v3 por
	// omissão, pelo que este é o caso NORMAL — não uma configuração exótica.
	if versaoDeEscritaDoWORM(epoca) < audit.SchemaV5 {
		linhas = append(linhas, fmt.Sprintf("rotacao de pinos: ATENCAO — o WORM escreve v%d e o `mandate_signer` SO entra no selo a partir da v%d. "+
			"O passo de confirmacao (`aos audit-trail --run <run> | grep signer=`) NAO TEM O QUE LER, e fechar a janela sem ele e fecha-la as cegas. "+
			"Ligue AOS_AUDIT_WRITE_SCHEMA=5 ANTES de abrir a janela. Consequencia gemea, na mesma epoca: a guarda da retoma compara "+
			"`rec.Principal.MandateSigner`, que abaixo da v%d fica VAZIO — um run suspenso sob o pino antigo retoma sob o novo sem dizer nada",
			versaoDeEscritaDoWORM(epoca), audit.SchemaV5, audit.SchemaV5))
	}

	// B4 — AS DUAS JANELAS ABERTAS AO MESMO TEMPO.
	//
	// Um mandato v1 assinado pelo pino ACABADO DE ACRESCENTAR é aceite, e sob um v1 o emissor age
	// por QUALQUER submissor — contorna os `requesters` do AOS-439. Não se recusa aqui (em
	// produção o mandato vivo É v1, e recusar partia a rotação no estado actual); declara-se, e
	// o procedimento manda fechar a janela dos v1 primeiro.
	if !v1Ate.IsZero() && agora.Before(v1Ate) {
		linhas = append(linhas, "rotacao de pinos: ATENCAO — a janela dos MANDATOS v1 tambem esta aberta (ate "+
			v1Ate.UTC().Format(time.RFC3339)+"). Um mandato v1 assinado pelo pino ACABADO DE ACRESCENTAR e aceite, e sob um v1 o "+
			"emissor age por QUALQUER submissor: a rotacao abre, durante estes dias, um caminho para contornar os `requesters` "+
			"(AOS-439). FECHE primeiro a janela dos v1 (re-assine o mandato com --requesters e ponha AOS_MANDATE_V1_UNTIL no passado), "+
			"e so depois rode o pino")
	}
	return linhas
}

// mandatoFIDO2PostureBanner declara quantos dos humanos pinados assinam em HARDWARE (AOS-446
// fase 1). Um pino de software é a seed em ficheiro que o ADR-033 §6.1 mediu — copia-se com um
// `cat`, e o mandato passa a valer o que valer esse ficheiro. Diz-se no arranque, com o número,
// porque a postura que não se declara é a que ninguém confere.
func mandatoFIDO2PostureBanner(iss string, signers map[string][]identity.MandateSigner) []string {
	if iss == "" {
		return nil
	}
	hw, total := pinosDeHardware(signers)
	if total == 0 {
		return nil
	}
	if hw == total {
		return []string{fmt.Sprintf("mandatos FIDO2 (AOS-446 fase 1): os %d pino(s) em vigor sao chaves de "+
			"HARDWARE (sk-ssh-ed25519) — cada mandato exige um toque no autenticador e o no RECUSA uma assinatura "+
			"sem o bit de presenca de utilizador (que o proprio `ssh-keygen -Y verify` aceita sem exigir). "+
			"O que ISTO nao prova, e nenhum pino prova, e que a chave privada tenha nascido no autenticador: "+
			"uma chave de software embrulhada num blob sk-ssh tem a mesma forma. Prova-o a cerimonia de geracao "+
			"(`ssh-keygen -t ed25519-sk -O resident`), fora deste processo", total)}
	}
	return []string{fmt.Sprintf("mandatos FIDO2 (AOS-446 fase 1): %d de %d pino(s) em vigor sao chaves de "+
		"HARDWARE; os outros %d assinam com uma SEED EM FICHEIRO, em hex e em claro na maquina deles (ADR-033 §6.1) "+
		"— quem copiar esse ficheiro assina mandatos para sempre. Para passar um humano a hardware: "+
		"ssh-keygen -t ed25519-sk -O application=ssh:aos-mandate, e trocar o pino dele em AOS_MANDATE_SIGNERS pela "+
		"linha da chave publica", hw, total, total-hw)}
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
