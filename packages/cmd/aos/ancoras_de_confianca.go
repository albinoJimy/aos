package main

// AS ÂNCORAS DE CONFIANÇA FICAM SELADAS NO ARRANQUE (AOS-446 fase 1, ADR-033 §6.3 e §8).
//
// # O DEFEITO QUE ISTO FECHA
//
// O `.env` do utilizador `aos` traz TODAS as raízes de confiança do nó, lado a lado:
// `AOS_MANDATE_SIGNERS` (a chave do humano que assina mandatos), `AOS_ISSUER_PUBKEY` (o emissor
// manual, confiado por inteiro — trocá-lo cunha qualquer raiz humana sem mandato nenhum),
// `AOS_MANDATED_ISSUER_PUBKEY`, `AOS_OPERATORS` (steer/pause, autonomy:set, dsar:erase),
// `AOS_RATIFIERS`, `AOS_POLICY_TRUST_ANCHOR` (troque-a e o bundle da política passa a ser o seu),
// `AOS_WORM_TRUST_ANCHOR` e o `secrets/approvers.json` do *four-eyes*. Quem tem root no host — e o
// ADR-033 §6.1 conta seis caminhos para lá, não um — troca qualquer uma, reinicia, e **nada no
// registo o denuncia**. O nó passa a servir sob outra autoridade com o mesmo aspecto de antes.
//
// # O QUE ISTO FAZ, E SÓ ISTO
//
// No arranque, DEPOIS de a hash-chain ter sido re-verificada e ancorada, o nó sela na partição
// `trust-anchors` do WORM um registo com a IMPRESSÃO DIGITAL de cada âncora em uso. Não a chave: o
// digest chega para detectar a troca, e é curto o suficiente para o operador o comparar de cabeça.
//
// O que isto NÃO é: não impede a troca. Quem escreve o `.env` continua a poder trocá-la, e quem
// escreve o ficheiro do WORM pode acrescentar-lhe registos (o `EntryHash` é um SHA-256 SEM chave).
// O que fecha é a SUPRESSÃO a jusante, e é aí que está o valor: a âncora diária, assinada FORA do
// host pelo `aos-issuer worm-seal` com uma chave que não está lá, congela o que o registo dizia. A
// partir do momento em que uma selagem cobre o registo, quem trocar a âncora ou apaga o que já
// está ancorado — e a continuidade recusa selar sobre isso ([ErrWormSealRecuo]/
// [ErrWormSealDivergencia]) — ou deixa lá a prova da troca, e a selagem seguinte denuncia-a.
//
// # SELA-SE SEMPRE, E NÃO SÓ QUANDO MUDA
//
// É o argumento S-02 do changelog de política (`policy_changelog.go`), e vale aqui letra por
// letra: decidir escrever a partir do que se lê da própria partição dá a quem escreve no ficheiro
// um BOTÃO DE SILENCIAMENTO — pré-plantar um registo com as impressões que vai instalar faz o nó
// concluir «igual ao último» e a troca real nunca é registada. Escrevendo sempre, o pior que um
// adversário consegue é acrescentar ruído; nunca apagar a afirmação do próprio nó.
//
// O tipo do registo distingue as duas leituras: `trust_anchors.changed` quando alguma impressão
// difere do último registo, `trust_anchors.active` quando o arranque confirma o que já lá estava.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// A FORMA do registo — a partição, os tipos de evento, os parâmetros e o digest — vive em
// `platform/audit` ([audit.TrustAnchors]), e não aqui, porque tem DOIS leitores em módulos
// diferentes: este, que escreve, e o `aos-issuer worm-seal`, que lê fora do host para denunciar a
// troca. Duas definições divergentes dariam um selador verde a varrer nada. O que fica AQUI é o
// CÁLCULO das impressões, que precisa da Config e só o nó tem.
//
// Aliases locais para o resto do ficheiro se ler sem ruído.
const (
	// TrustAnchorsPartition é a partição do WORM onde o registo vive.
	TrustAnchorsPartition = audit.TrustAnchorsPartition
	// TrustAnchorsChangedEventType / TrustAnchorsActiveEventType distinguem transição de
	// confirmação, para que «mudou» não passe a significar «arrancou».
	TrustAnchorsChangedEventType = audit.TrustAnchorsChangedEventType
	TrustAnchorsActiveEventType  = audit.TrustAnchorsActiveEventType
	// ancoraAusente é o valor de uma âncora que NÃO está configurada — selado explicitamente.
	ancoraAusente = audit.TrustAnchorAusente
)

// trustAnchorsActor é a atribuição do selo: quem trocou as âncoras foi quem escreveu o `.env` e
// reiniciou. O nó não inventa um humano — mesma disciplina de [policyProvisionActor].
const trustAnchorsActor = "config:node"

// trustAnchorsCapability é o direito nominal exercido pelo registo, no molde do changelog.
const trustAnchorsCapability = "audit:trust-anchors"

// trustAnchorsFromParams é o leitor de [audit.TrustAnchorsFromParams], re-exposto com o nome
// local que os testes deste pacote usam.
func trustAnchorsFromParams(p map[string]string) (trustAnchors, string) {
	return audit.TrustAnchorsFromParams(p)
}

// ErrTrustAnchorsProvisioning — o nó não conseguiu ler ou selar o registo das âncoras. Fail-closed
// no molde de [ErrPolicyProvisioning]: um nó que serve sob âncoras que não consegue registar é
// exactamente o rasto em falta que este ficheiro existe para produzir.
var ErrTrustAnchorsProvisioning = errors.New("aos: registo das ancoras de confianca (AOS-446) falhou — nao foi possivel ler a particao `trust-anchors` do WORM ou selar o registo; o no recusa arrancar sem deixar rasto das ancoras sob as quais vai servir")

// impressaoDeChave é a impressão digital de uma pubkey ed25519 avulsa. O domínio prende-a a este
// uso: o mesmo material hashado noutro sítio não produz o mesmo valor.
func impressaoDeChave(dominio string, pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ancoraAusente
	}
	sum := sha256.Sum256(append([]byte("aos.trust-anchor."+dominio+":"), pub...))
	return hex.EncodeToString(sum[:])[:32]
}

// impressaoDeConjunto resume um conjunto NOMEADO de chaves (operadores, ratificadores,
// aprovadores). A ordenação é obrigatória: um mapa não tem ordem, e um digest que dependesse dela
// acusaria uma troca a cada arranque.
func impressaoDeConjunto(dominio string, entradas map[string]ed25519.PublicKey) string {
	if len(entradas) == 0 {
		return ancoraAusente
	}
	nomes := make([]string, 0, len(entradas))
	for n := range entradas {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)
	h := sha256.New()
	fmt.Fprintf(h, "aos.trust-anchor.%s:%d;", dominio, len(nomes))
	for _, n := range nomes {
		fmt.Fprintf(h, "%d:%s=%s;", len(n), n, impressaoDeChave(dominio, entradas[n]))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// trustAnchors é o retrato das âncoras em uso, já reduzido a impressões.
type trustAnchors = audit.TrustAnchors

// ancorasDaConfig deriva o retrato da Config COMPOSTA — e não do ambiente. É deliberado: o que
// interessa registar é sob que âncoras o nó VAI SERVIR, e quem as fornece pode ser o ambiente ou
// o composition-root. Ler o `os.Getenv` aqui registaria a intenção e não o efeito, que é a classe
// de defeito que o §6.3 do ADR-033 mediu (uma variável descrita e outra em uso).
//
// A ÂNCORA DA POLÍTICA É A DO PDP COMPOSTO (achado A4 da revisão adversarial, 2026-09-27). A
// primeira versão punha em [Config.PolicyTrustAnchor] o que `os.Getenv("AOS_POLICY_TRUST_ANCHOR")`
// dava — o que contradizia esta mesma nota, e pior: com um bundle aberto sem `WithTrustAnchor`, a
// âncora efectiva vem do directório do bundle, e o registo selava outra coisa. O campo passou a
// ser preenchido por [pdp.PDP.TrustAnchor] na fronteira de ambiente, pelo que o que se lê aqui é
// a chave que verificou o bundle em uso.
func ancorasDaConfig(cfg Config) trustAnchors {
	a := trustAnchors{
		// O emissor MANUAL: confiado por inteiro, sem mandato. A âncora mais poderosa do conjunto.
		"issuer_pubkey": impressaoDeChave("issuer", cfg.IssuerPubKey),
		// O emissor AUTOMÁTICO e os humanos que o limitam.
		"mandated_issuer_pubkey": impressaoDeChave("mandated-issuer", cfg.MandatedIssuerPubKey),
		"mandate_signers":        impressaoDosAssinantes(cfg.MandateSigners),
		// Os operadores (steer/pause, autonomy:set, dsar:erase) e os ratificadores da promoção.
		"operators": impressaoDeConjunto("operator", cfg.Operators),
		"ratifiers": impressaoDosRatificadores(cfg.Ratifiers),
		// O four-eyes.
		"approvers": impressaoDosAprovadores(cfg.Approvers),
		// A âncora do bundle de política e a do selador do WORM.
		"policy_trust_anchor": impressaoDeChave("policy", cfg.PolicyTrustAnchor),
		"worm_trust_anchor":   impressaoDaAncoraDoWORM(cfg.WORMAnchor),
		// AS JANELAS SÃO AUTORIDADE, E POR ISSO SÃO ÂNCORAS (achado A3 da revisão adversarial,
		// 2026-09-27). Sob um mandato v1 o emissor age por QUALQUER submissor; com dois pinos em
		// vigor, duas chaves assinam mandatos. Root que estenda qualquer uma das duas datas
		// alarga a autoridade do nó sem tocar em chave nenhuma — e, antes disto, o digest ficava
		// byte a byte igual. Não são chaves: sela-se o VALOR, que o digest cobre de graça.
		"mandate_v1_until":       instanteOuFechada(cfg.MandateV1Until),
		"mandate_dual_pin_until": instanteOuFechada(cfg.MandateDualPinUntil),
	}
	return a
}

// instanteOuFechada é a forma selada de uma janela: o instante em UTC, ou `(fechada)`.
func instanteOuFechada(t time.Time) string {
	if t.IsZero() {
		return "(fechada)"
	}
	return t.UTC().Format(time.RFC3339)
}

// impressaoDosAssinantes usa a impressão CANÓNICA do pino ([identity.MandateSigner.Fingerprint]) e
// não uma calculada aqui: é a MESMA que vai no selo de cada decisão, e duas fórmulas para a mesma
// coisa é como se faz um registo que não bate com o outro.
func impressaoDosAssinantes(signers map[string][]identity.MandateSigner) string {
	if len(signers) == 0 {
		return ancoraAusente
	}
	return identity.MandateSignersDigest(signers)[:32]
}

func impressaoDosRatificadores(rs []RatifierConfig) string {
	if len(rs) == 0 {
		return ancoraAusente
	}
	m := make(map[string]ed25519.PublicKey, len(rs))
	for _, r := range rs {
		m[r.Principal] = r.PubKey
	}
	return impressaoDeConjunto("ratifier", m)
}

// impressaoDosAprovadores inclui a AUTORIDADE de cada aprovador, e não só a chave: mudar um
// aprovador de `approve:safe` para `approve:danger` é uma mudança de âncora tão real como trocar
// a chave dele, e um digest só das chaves não a veria.
func impressaoDosAprovadores(as []ApproverConfig) string {
	if len(as) == 0 {
		return ancoraAusente
	}
	tipo := make([]string, 0, len(as))
	for _, a := range as {
		auth := append([]string(nil), a.Authority...)
		sort.Strings(auth)
		tipo = append(tipo, fmt.Sprintf("%s=%s[%s]", a.Principal, impressaoDeChave("approver", a.PubKey), strings.Join(auth, "|")))
	}
	sort.Strings(tipo)
	h := sha256.New()
	fmt.Fprintf(h, "aos.trust-anchor.approver:%d;", len(tipo))
	for _, t := range tipo {
		fmt.Fprintf(h, "%d:%s;", len(t), t)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// O PISO DE FRESCURA DO WORM (`AOS_WORM_EXPECTED_HEAD(S_FILE)`) e o ficheiro de checkpoints NÃO
// entram no retrato, e é uma decisão e não um esquecimento (achado A3 da revisão adversarial,
// 2026-09-27).
//
// A `AOS_WORM_TRUST_ANCHOR` entra porque é uma CHAVE: trocá-la faz o nó aceitar checkpoints
// assinados por outra pessoa, e isso é uma mudança de autoridade que dura. O piso e os
// checkpoints são outra coisa — são o ESTADO da ancoragem, e mudam legitimamente a cada selagem
// diária. Selá-los faria o registo declarar uma «troca de âncora» todos os dias, e um sinal que
// dispara todos os dias é um sinal que ninguém lê: seria destruir este registo para cobrir um
// vector que já está fechado noutro sítio. Baixar o piso indevidamente (reapresentar um
// checkpoint legítimo mas anterior) é um ataque real, e quem o recusa é o
// [audit.VerifyFromCheckpointAtHead] no arranque — com `ErrCheckpointStale` — não este registo.
func impressaoDaAncoraDoWORM(a *WormAnchor) string {
	if a == nil {
		return ancoraAusente
	}
	return impressaoDeChave("worm", a.Public)
}

// trustAnchorsState é o que o arranque decidiu, para o banner.
type trustAnchorsState struct {
	sealed    bool
	transicao bool
	digest    string
	anterior  string
	mudancas  []string
	// primeiro é verdadeiro quando a partição estava vazia: não há nada com que comparar, e
	// dizê-lo é mais honesto do que chamar «transição» ao primeiro registo de sempre.
	primeiro bool
}

// provisionTrustAnchors sela o retrato das âncoras. `worm` nil ⇒ recusa: sem store não há selo.
func provisionTrustAnchors(ctx context.Context, worm audit.Store, cfg Config, now time.Time) (trustAnchorsState, error) {
	var st trustAnchorsState
	if worm == nil {
		return st, fmt.Errorf("%w: sem WORM composto", ErrTrustAnchorsProvisioning)
	}
	anterior, digestAnterior, _, achou, err := audit.UltimasAncoras(ctx, worm, 0)
	if err != nil {
		return st, fmt.Errorf("%w: ler particao %q: %v", ErrTrustAnchorsProvisioning, TrustAnchorsPartition, err)
	}
	if !achou {
		anterior = nil
	}
	agora := ancorasDaConfig(cfg)
	st.digest = agora.Digest()
	st.anterior = digestAnterior
	st.primeiro = anterior == nil
	st.transicao = !st.primeiro && digestAnterior != st.digest
	if !st.primeiro {
		st.mudancas = agora.Diferencas(anterior)
	}

	tipo := TrustAnchorsActiveEventType
	if st.transicao || st.primeiro {
		tipo = TrustAnchorsChangedEventType
	}
	rec := audit.AuditRecord{
		Partition: TrustAnchorsPartition,
		Timestamp: now.UTC(),
		Decision:  audit.DecisionAllow,
		Principal: audit.Principal{NHIID: trustAnchorsActor},
		// Code/Reason não são atribuição de recusa aqui, mas o v3 sela-os e são o sítio onde um
		// auditor lê o que aconteceu sem desmontar as obrigações.
		Code:       tipo,
		Reason:     razaoDoRegisto(st),
		Capability: trustAnchorsCapability,
		Resource:   audit.Resource{Type: tipo, Value: st.digest},
		Obligations: []audit.Obligation{{
			Type:   tipo,
			Fields: agora.Diferencas(anterior),
			Params: agora.Params(trustAnchorsActor),
		}},
	}
	if _, err := worm.Append(ctx, rec); err != nil {
		return st, fmt.Errorf("%w: selar %s (%s->%s): %v", ErrTrustAnchorsProvisioning, tipo, digestAnterior, st.digest, err)
	}
	st.sealed = true
	return st, nil
}

func razaoDoRegisto(st trustAnchorsState) string {
	switch {
	case st.primeiro:
		return "primeiro registo desta particao — nao ha ancoras anteriores com que comparar"
	case st.transicao:
		return "as ancoras de confianca MUDARAM face ao ultimo arranque registado"
	default:
		return "confirmacao no arranque: ancoras inalteradas face ao ultimo registo"
	}
}

// trustAnchorsBanner declara o que o arranque selou — derivado do estado, não da intenção.
func trustAnchorsBanner(st trustAnchorsState) []string {
	if !st.sealed {
		return nil
	}
	base := fmt.Sprintf("ancoras de confianca (AOS-446 fase 1): SELADAS na particao %q — digest %s. "+
		"Cobre o emissor manual, o emissor mandatado, os assinantes de mandatos, os operadores, os ratificadores, "+
		"os aprovadores do four-eyes, a ancora da politica, a do selador do WORM, e as duas JANELAS que tambem sao "+
		"autoridade (mandate_v1_until, mandate_dual_pin_until). "+
		"`aos audit-trail --run %s` le o historico; `aos-issuer worm-seal` recusa selar se mudarem sem o operador o declarar",
		TrustAnchorsPartition, st.digest, TrustAnchorsPartition)
	switch {
	case st.primeiro:
		return []string{base + ". PRIMEIRO REGISTO desta particao: nao ha nada com que comparar, e por isso este " +
			"arranque NAO prova que as ancoras nao mudaram antes dele — so a partir da proxima selagem e que a troca passa a ter rasto"}
	case st.transicao:
		return []string{base + fmt.Sprintf(". ATENCAO — MUDARAM face ao ultimo arranque registado (%s): %s. Se nao foi o "+
			"operador a roda-las, o `.env` deste host foi reescrito", st.anterior, strings.Join(st.mudancas, "; "))}
	default:
		return []string{base + ". Inalteradas face ao ultimo registo (" + strconv.Itoa(len(st.mudancas)) + " diferencas)"}
	}
}
