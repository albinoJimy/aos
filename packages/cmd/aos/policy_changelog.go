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
)

// policyChangelogMaxSeq é o teto da leitura da partição — o mesmo de `aos audit-trail`.
const policyChangelogMaxSeq = 1 << 40

// ErrPolicyProvisioning — o nó não conseguiu confirmar ou selar a troca de política no arranque.
// Fail-closed no molde de [ErrAutonomyProvisioning]: uma política em vigor sem changelog selado é
// exactamente o rasto em falta que AOS-088 existe para impedir.
var ErrPolicyProvisioning = errors.New("aos: changelog de politica (AOS-310) falhou — nao foi possivel ler a particao `policy` do WORM ou selar `policy.changed` para o bundle carregado; o no recusa arrancar com uma troca de politica que nao consegue auditar")

// policyChangelogState é o que o arranque decidiu sobre o changelog, para o banner.
type policyChangelogState struct {
	// sealed é verdadeiro se ESTE arranque selou uma transição (versão/hash diferentes do último selo).
	sealed bool
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
	if lastVersion == p.Version() && lastHash == p.ContentHash() {
		return st, nil // mesma política que o último selo: arranque idempotente, nada a selar.
	}
	ev := pdp.PolicyChangeEvent{
		OldVersion:  lastVersion,
		NewVersion:  p.Version(),
		ContentHash: p.ContentHash(),
		Author:      policyProvisionActor,
		Reason:      policyProvisionReason,
		At:          now.UTC(),
	}
	if _, err := worm.Append(ctx, pdp.BuildPolicyChangedRecord(ev, "")); err != nil {
		return st, fmt.Errorf("%w: selar %s->%s: %v", ErrPolicyProvisioning, lastVersion, p.Version(), err)
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
	if st.sealed {
		de := st.previous
		if de == "" {
			de = "(nenhuma — primeiro selo desta particao)"
		}
		return []string{fmt.Sprintf("changelog de politica (AOS-310): TRANSICAO SELADA no arranque na particao %q — policy.changed %s -> %s (content_hash %s, actor %q): a troca de bundle por reinicio deixa de ser rasto so em log volatil; `aos audit-trail --run %s` le o historico", pdp.DefaultPolicyPartition, de, p.Version(), p.ContentHash(), policyProvisionActor, pdp.DefaultPolicyPartition)}
	}
	return []string{fmt.Sprintf("changelog de politica (AOS-310): politica em vigor %s (content_hash %s) coincide com o ULTIMO policy.changed selado na particao %q — nada selado neste arranque (idempotente)", p.Version(), p.ContentHash(), pdp.DefaultPolicyPartition)}
}
