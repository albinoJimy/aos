package main

// CHANGELOG DE POLÍTICA NO ARRANQUE (AOS-310).
//
// O DEFEITO QUE ISTO FECHA. O `policy.changed` — o changelog selado na hash-chain WORM que AOS-088
// promete para cada alteração de política (versão antiga → nova, autor, motivo, content_hash) —
// só era escrito por [pdp.PDP.Reload]. E `Reload` não tem chamador de produção nem rota HTTP: no
// nó entregue, trocar de política é substituir o directório do bundle e REINICIAR, que passa por
// [pdp.Open], que não sela nada. Uma troca real de política deixava rasto só na linha de banner
// «politica em vigor versao …», em log volátil (`analises/10` §3.3). O `policy_version` de cada
// mediação continua a ir para o WORM — a mudança era DETECTÁVEL no trilho de mediação — mas sem
// QUANDO, QUEM, nem o hash antigo/novo, que é precisamente o que o changelog transporta.
//
// O PRECEDENTE está no mesmo composition-root, vinte linhas acima: AOS-248 sela os níveis de
// autonomia no arranque ([autonomyWiring.provision]) e recusa arrancar se a selagem falhar. Fechou
// a classe para a autonomia e não para a política. Este ficheiro é a transposição directa.
//
// O QUE FAZ, e só isto: com um bundle carregado, lê a partição `policy` do WORM, encontra a
// última transição selada e, se a (versão, content_hash) em vigor for DIFERENTE, sela um
// `policy.changed` com autor `config:node` — a atribuição honesta: quem trocou o bundle foi quem
// o pôs no directório e reiniciou; o nó não inventa um humano. Igual ⇒ nada (arranques
// idempotentes não incham a cadeia). Falha ⇒ o nó NÃO arranca a servir uma política cuja troca não
// conseguiu registar.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/control-plane/pdp"
	audit "github.com/aos-ref/platform/audit"
)

// policyProvisionActor / policyProvisionReason são a atribuição e o motivo do selo de arranque.
// Espelham [autonomyProvisionActor]/[autonomyProvisionReason] pela mesma razão: o nó regista
// quem decidiu (o deployment), não um humano que não participou.
const (
	policyProvisionActor  = "config:node"
	policyProvisionReason = "carregamento no arranque (AOS_POLICY_BUNDLE_DIR)"
	// policyConfirmReason é o motivo do registo de CONFIRMAÇÃO — o arranque que serviu a MESMA
	// política do último selo. Distinto do de transição para que os dois se leiam sem ambiguidade.
	policyConfirmReason = "confirmacao no arranque: politica inalterada face ao ultimo selo"
	// policyActiveEventType é o rótulo do registo de confirmação. Vive AQUI, e não no pacote
	// `pdp`, porque é um facto do ARRANQUE DO NÓ (que política este processo serviu) e não uma
	// alteração de política — que é o que o `pdp` modela e sela em [pdp.PolicyChangedEventType].
	policyActiveEventType = "policy.active"
)

// policyChangelogMaxSeq é o teto da leitura da partição — o mesmo de `aos audit-trail`.
const policyChangelogMaxSeq = 1 << 40

// ErrPolicyProvisioning — o nó não conseguiu confirmar ou selar a troca de política no arranque.
// Fail-closed no molde de [ErrAutonomyProvisioning]: uma política em vigor sem changelog selado é
// exactamente o rasto em falta que AOS-088 existe para impedir.
var ErrPolicyProvisioning = errors.New("aos: changelog de politica (AOS-310) falhou — nao foi possivel ler a particao `policy` do WORM ou selar `policy.changed` para o bundle carregado; o no recusa arrancar com uma troca de politica que nao consegue auditar")

// policyChangelogState é o que o arranque decidiu sobre o changelog, para o banner.
type policyChangelogState struct {
	// sealed é verdadeiro se ESTE arranque escreveu na partição — o que, desde o fecho de S-02,
	// é SEMPRE que há política carregada. Continua a existir porque distingue «não havia bundle»
	// (nada a registar) de «registou-se».
	sealed bool
	// transicao distingue as duas formas do que foi selado: `policy.changed` (a versão ou o hash
	// mudaram face ao último selo) de `policy.active` (a confirmação de que este arranque serviu
	// a mesma política). É o que o banner usa para não chamar «mudança» a um reinício.
	transicao bool
	// previous é a versão anterior encontrada no WORM ("" se a partição estava vazia).
	previous string
}

// provisionPolicyChangelog compara a política em vigor com o último `policy.changed` selado e,
// se diferir, sela a transição. p nil ou não carregado ⇒ no-op (não há política a registar — o
// banner já declara o PDP não-carregado). worm nil ⇒ recusa: sem store não há selo.
func provisionPolicyChangelog(ctx context.Context, worm audit.Store, p *pdp.PDP, now time.Time) (policyChangelogState, error) {
	var st policyChangelogState
	if p == nil || p.Version() == "" {
		return st, nil
	}
	if worm == nil {
		return st, fmt.Errorf("%w: sem WORM composto", ErrPolicyProvisioning)
	}
	recs, err := worm.Read(ctx, pdp.DefaultPolicyPartition, 1, policyChangelogMaxSeq)
	if err != nil {
		return st, fmt.Errorf("%w: ler particao %q: %v", ErrPolicyProvisioning, pdp.DefaultPolicyPartition, err)
	}
	// A última transição selada é a que interessa; percorre-se de trás para a frente e pára-se
	// no primeiro `policy.changed` — a partição é da política, mas ser estrito no tipo custa nada
	// e protege de um registo de outra natureza que um dia lá caia.
	var lastVersion, lastHash string
	for i := len(recs) - 1; i >= 0; i-- {
		found := false
		for _, o := range recs[i].Obligations {
			if o.Type == pdp.PolicyChangedEventType {
				lastVersion, lastHash = o.Params["new_version"], o.Params["content_hash"]
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	st.previous = lastVersion
	st.transicao = lastVersion != p.Version() || lastHash != p.ContentHash()

	// O NÓ SELA SEMPRE O QUE ESTÁ EM VIGOR, e é isso que fecha a supressão (achado S-02).
	//
	// A primeira versão decidia SELAR ou NÃO SELAR a partir do que lia da própria partição: se a
	// (versão, hash) coincidisse com o último `policy.changed`, considerava o arranque idempotente
	// e não escrevia nada. Isso dava a quem tem escrita no ficheiro do WORM um botão de
	// SILENCIAMENTO: pré-plantar um registo com a versão e o hash do bundle que vai instalar faz o
	// nó concluir «igual ao último selo» e a troca real nunca é registada — com o banner a
	// declarar «idempotente», que é pior do que o silêncio.
	//
	// A partição `policy` continua sem raiz de confiança fora do ficheiro (o `EntryHash` é um
	// SHA-256 sem chave e a verificação ancorada não cobre o que é apendido depois do último
	// checkpoint — o eixo é DEF-268, a custódia da chave de selagem, que este repositório não
	// tem). O que se pode fechar sem essa chave é a metade que interessa: **o registo do nó não
	// pode ser suprimido por quem escreve antes dele**. Escrevendo SEMPRE, o pior que um adversário
	// consegue é acrescentar ruído — nunca apagar a afirmação do próprio nó sobre o que está a
	// servir.
	//
	// O tipo de evento distingue as duas coisas, para que «mudou» não passe a significar «arrancou»:
	//   - TRANSIÇÃO (versão ou hash diferentes do último selo) ⇒ `policy.changed`, como antes;
	//   - CONFIRMAÇÃO (iguais) ⇒ `policy.active`, o registo de que ESTE arranque serviu ESTA
	//     política. Um auditor obtém uma linha contínua de qual política esteve em vigor a cada
	//     arranque, e a ausência de uma confirmação passa a ser ela própria um sinal.
	//
	// Custo: um registo por arranque numa partição própria. Arranques são raros e a partição é
	// gapless e verificável de forma independente; a alternativa — não escrever — é o buraco.
	ev := pdp.PolicyChangeEvent{
		OldVersion:  lastVersion,
		NewVersion:  p.Version(),
		ContentHash: p.ContentHash(),
		Author:      policyProvisionActor,
		Reason:      policyProvisionReason,
		At:          now.UTC(),
	}
	rec := pdp.BuildPolicyChangedRecord(ev, "")
	if !st.transicao {
		ev.Reason = policyConfirmReason
		rec = pdp.BuildPolicyChangedRecord(ev, "")
		// A obrigação e o recurso passam a nomear a CONFIRMAÇÃO. Reutiliza-se o construtor do
		// `pdp` — em vez de montar um registo à mão aqui — para que a forma dos dois eventos não
		// possa divergir: um auditor lê-os com o mesmo parser e o campo que os separa é o tipo.
		rec.Obligations[0].Type = policyActiveEventType
		rec.Resource.Type = policyActiveEventType
	}
	if _, err := worm.Append(ctx, rec); err != nil {
		return st, fmt.Errorf("%w: selar %s (%s->%s): %v", ErrPolicyProvisioning, rec.Obligations[0].Type, lastVersion, p.Version(), err)
	}
	st.sealed = true
	return st, nil
}

// policyChangelogBanner declara o que o arranque fez com o changelog — derivado do estado, não da
// intenção, como o resto de posture_banner.go.
func policyChangelogBanner(p *pdp.PDP, st policyChangelogState) []string {
	if p == nil || p.Version() == "" {
		return nil // o PDP não-carregado já tem a sua linha.
	}
	if st.transicao {
		de := st.previous
		if de == "" {
			de = "(nenhuma — primeiro selo desta particao)"
		}
		return []string{fmt.Sprintf("changelog de politica (AOS-310): TRANSICAO SELADA no arranque na particao %q — policy.changed %s -> %s (content_hash %s, actor %q): a troca de bundle por reinicio deixa de ser rasto so em log volatil; `aos audit-trail --run %s` le o historico", pdp.DefaultPolicyPartition, de, p.Version(), p.ContentHash(), policyProvisionActor, pdp.DefaultPolicyPartition)}
	}
	return []string{fmt.Sprintf("changelog de politica (AOS-310): CONFIRMACAO SELADA no arranque na particao %q — %s %s (content_hash %s): a politica coincide com o ultimo selo, e o no regista-o na mesma. Selar SEMPRE e o que impede que quem escreve no ficheiro do WORM pre-plante um registo com esta versao e este hash para fazer o no concluir «idempotente» e NAO registar uma troca real (S-02)", pdp.DefaultPolicyPartition, policyActiveEventType, p.Version(), p.ContentHash())}
}
