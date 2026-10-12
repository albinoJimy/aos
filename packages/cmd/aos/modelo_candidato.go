package main

// modelo_candidato.go — O TITULAR, O CANDIDATO E O DESPACHANTE (AOS-520, ADR-041 §2.1, §2.5, §2.7).
//
// O nó serve o TITULAR — o modelo de `AOS_MODEL_NAME`, o de sempre — e, no máximo, um CANDIDATO: o
// perfil de um registo de entrada assinado (AOS-519) cujo nome pedido não é o do titular. Um
// registo assinado para o nome do titular não é um candidato: substitui o perfil do titular.
//
// INERTE POR OMISSÃO. `AOS_MODEL_CANDIDATE=off|on`, omissão `off`. O candidato só está A SERVIR
// com o interruptor a `on`, exactamente um registo não-titular na pasta, e a fatia assinada maior
// que zero. Fora disso nenhum run novo passa pela função de escolha nem pelo livro do canary.
//
// UM GATEWAY, UM ADAPTADOR POR ALIAS. O gateway não tem o modelo na construção; o que guarda o
// alias é o adaptador RT→GW. Havendo um perfil assinado não-titular carregado, o cliente de modelo
// do nó passa a ser um DESPACHANTE sobre vários adaptadores do MESMO gateway, e escolhe pelo alias
// que a hospedagem do run pôs no contexto ([withModeloDoRun]). O despachante existe mesmo com o
// interruptor a `off`, para um run já fixado no candidato poder acabar enquanto o perfil estiver
// carregado; o interruptor só controla atribuições novas.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// Os valores de `AOS_MODEL_CANDIDATE`. Vocabulário FECHADO.
const (
	modelCandidateOff = "off"
	modelCandidateOn  = "on"
)

// ErrBadModelCandidate — `AOS_MODEL_CANDIDATE` tem um valor fora do vocabulário. Fail-closed: o nó
// não arranca a adivinhar se o candidato serve ou não.
var ErrBadModelCandidate = errors.New("aos: AOS_MODEL_CANDIDATE invalida — os valores sao \"off\" (a omissao: nenhum candidato e servido) e \"on\" (o candidato do registo de entrada assinado e servido na fatia assinada)")

// ErrModelCandidateMoreThanOne — `AOS_MODEL_CANDIDATE=on` e a pasta de perfis assinados tem mais de
// um registo que não é o do alias do titular. O nó serve um titular e NO MÁXIMO um candidato
// (decisão D6 do dono). Fail-closed: não arranca; os rótulos dos registos vêm a seguir.
var ErrModelCandidateMoreThanOne = errors.New("aos: AOS_MODEL_CANDIDATE=on com mais de um candidato — a pasta de perfis de rota assinados tem mais de um registo que nao e o do alias do titular (AOS_MODEL_NAME); o no serve um titular e no maximo um candidato. Retire registos ate ficar um, ou ponha AOS_MODEL_CANDIDATE=off")

// ErrModelCandidateWithoutPrice — `AOS_MODEL_CANDIDATE=on`, a contabilidade de custo está composta
// (o par do titular tem preço) e o par do candidato NÃO tem preço na tabela em vigor. Fail-closed:
// a contabilidade recusa uma chamada a um par sem preço, e o candidato falharia todos os turnos.
var ErrModelCandidateWithoutPrice = errors.New("aos: AOS_MODEL_CANDIDATE=on e o candidato nao tem preco na tabela em vigor para a regiao do no, com a contabilidade de custo composta — a contabilidade recusaria todas as chamadas do candidato. Acrescente o par a tabela (AOS_MODEL_PRICING_PATH), ou ponha AOS_MODEL_CANDIDATE=off")

// ErrModelNotServed — o run pede um alias que este nó não serve: nem é o do titular, nem o de um
// perfil assinado carregado. Fail-closed: o pedido NÃO vai para o titular.
var ErrModelNotServed = errors.New("aos: o modelo em que o run esta fixado nao e servido por este no (AOS-520)")

// modelCandidateFromEnv lê `AOS_MODEL_CANDIDATE`. Ausente, vazia ou `off` ⇒ false.
func modelCandidateFromEnv() (bool, error) {
	switch v := strings.TrimSpace(os.Getenv("AOS_MODEL_CANDIDATE")); v {
	case "", modelCandidateOff:
		return false, nil
	case modelCandidateOn:
		return true, nil
	default:
		return false, fmt.Errorf("%w: %q", ErrBadModelCandidate, v)
	}
}

// naoTitulares devolve os registos assinados cujo nome pedido não é o do titular, pela ordem do
// rótulo. São os candidatos que a pasta tem; com o interruptor a `on` só pode haver um.
func (p *perfisAssinados) naoTitulares(titular string) []routeentry.Entry {
	if p == nil {
		return nil
	}
	var out []routeentry.Entry
	for _, e := range p.entradas {
		if e.Profile.Requested != titular {
			out = append(out, e)
		}
	}
	return out
}

// candidatoAServir devolve o candidato que este nó SERVE, se servir algum: o interruptor a `on`,
// exactamente um registo não-titular, e a fatia assinada maior que zero (o segundo travão).
func (p *perfisAssinados) candidatoAServir(titular string) (candidatoEmVigor, bool) {
	if p == nil || !p.candidatoLigado {
		return candidatoEmVigor{}, false
	}
	nt := p.naoTitulares(titular)
	if len(nt) != 1 || nt[0].InitialSharePercent <= 0 {
		return candidatoEmVigor{}, false
	}
	e := nt[0]
	return candidatoEmVigor{alias: e.Profile.Requested, profileDigest: e.ProfileDigest, entryDigest: e.EntryDigest, fatia: e.InitialSharePercent}, true
}

// conferirCandidato são as conferências de ARRANQUE do candidato, com o interruptor já lido:
// no máximo um, e com preço quando a contabilidade de custo está composta. `comCusto` diz se está;
// `temPreco` diz se um alias tem preço na tabela em vigor para a região do nó. Com o interruptor a
// `off` não confere nada: a pasta pode ter vários registos não-titulares, e nenhum é servido.
func (p *perfisAssinados) conferirCandidato(titular string, comCusto bool, temPreco func(alias string) (bool, error)) error {
	if p == nil || !p.candidatoLigado {
		return nil
	}
	nt := p.naoTitulares(titular)
	if len(nt) > 1 {
		rotulos := make([]string, 0, len(nt))
		for _, e := range nt {
			rotulos = append(rotulos, e.Label)
		}
		sort.Strings(rotulos)
		return fmt.Errorf("%w: registos %q", ErrModelCandidateMoreThanOne, rotulos)
	}
	if len(nt) == 1 && comCusto {
		ok, err := temPreco(nt[0].Profile.Requested)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: registo %q, alias %q", ErrModelCandidateWithoutPrice, nt[0].Label, nt[0].Profile.Requested)
		}
	}
	return nil
}

// modelCandidateBanner declara o titular e o candidato. Com o interruptor ausente ou `off`, e sem registo
// não-titular nenhum (ou sem a pasta), não sai linha nenhuma: o arranque é o de antes.
func modelCandidateBanner(p *perfisAssinados, titular string) []string {
	if p == nil {
		// O interruptor está ligado e o nó não tem perfis assinados (sem a pasta, sem gateway por
		// ambiente, ou com um cliente injectado): não há candidato, e isso não fica em silêncio.
		// Com o interruptor ausente ou `off` não sai linha nenhuma.
		if ligado, err := modelCandidateFromEnv(); err == nil && ligado {
			return []string{"AVISO (EPIC-06/AOS-520): AOS_MODEL_CANDIDATE=on e este no NAO tem perfis de rota assinados carregados (AOS_MODEL_ROUTE_PROFILES_DIR por definir, ou o gateway de modelo nao foi composto a partir do ambiente) — nao ha candidato; todos os runs sao do titular. O candidato vem de um registo de entrada assinado nessa pasta"}
		}
		return nil
	}
	nt := p.naoTitulares(titular)
	if len(nt) == 0 {
		if p.candidatoLigado {
			return []string{fmt.Sprintf("modelo candidato (EPIC-06/AOS-520): AOS_MODEL_CANDIDATE=on e a pasta de perfis de rota assinados NAO tem nenhum registo para um alias que nao seja o do titular (%q) — nao ha candidato; todos os runs novos sao do titular", titular)}
		}
		return nil
	}
	if c, serve := p.candidatoAServir(titular); serve {
		return []string{fmt.Sprintf("modelo candidato A SERVIR (EPIC-06/AOS-520, ADR-041): titular %q; candidato %q (digest do perfil %s), do registo %q (digest do registo assinado %s); fatia assinada %d%% dos PLANOS NOVOS, ate %d planos atribuidos ao candidato. Quem escolhe e uma funcao do identificador do plano (%s), nao quem submete; o modelo fica fixado por run e todos os nos de um plano usam o mesmo. O candidato NAO passa a titular sozinho. O estado do canary (aberto ou terminado, e a contagem) vem numa linha propria, depois de o livro ser lido",
			titular, c.alias, c.profileDigest, nt[0].Label, c.entryDigest, c.fatia, canaryLimiteDePlanos, versaoDaEscolha)}
	}
	linhas := make([]string, 0, len(nt))
	for _, e := range nt {
		porque := "AOS_MODEL_CANDIDATE=off (a omissao)"
		if p.candidatoLigado {
			porque = "AOS_MODEL_CANDIDATE=on, mas a fatia assinada e 0%"
		}
		linhas = append(linhas, fmt.Sprintf("modelo candidato NAO SERVIDO (EPIC-06/AOS-520, ADR-041): titular %q; o registo %q traz um perfil assinado para o alias %q, que nao e o do titular (digest do perfil %s, digest do registo assinado %s, fatia assinada %d%%) — candidato nao servido: %s. Nenhum run novo lhe e atribuido. Um run que ja esteja fixado neste alias acaba com ele enquanto o perfil estiver carregado; um run novo de um plano que ja lhe tinha sido atribuido e RECUSADO (o plano falha, nao continua no titular)",
			titular, e.Label, e.Profile.Requested, e.ProfileDigest, e.EntryDigest, e.InitialSharePercent, porque))
	}
	return linhas
}

// ---------------------------------------------------------------------------
// O alias do run, no contexto, e o despachante
// ---------------------------------------------------------------------------

type modeloDoRunKey struct{}

// withModeloDoRun anexa ao contexto do run o alias em que o run está FIXADO POR ESCOLHA. Quem o
// anexa é a hospedagem ([NodeService.hostRun]), no contexto com que chama o runtime; um run não
// fixado por escolha não anexa nada, e o despachante dá-lhe o titular.
func withModeloDoRun(ctx context.Context, alias string) context.Context {
	if alias == "" {
		return ctx
	}
	return context.WithValue(ctx, modeloDoRunKey{}, alias)
}

// modeloDoRunFrom devolve o alias fixado no contexto do run, ou vazio.
func modeloDoRunFrom(ctx context.Context) string {
	alias, _ := ctx.Value(modeloDoRunKey{}).(string)
	return alias
}

// clientePorModelo é o DESPACHANTE: um adaptador RT→GW por alias, todos sobre o mesmo gateway.
//
//   - contexto sem alias ⇒ o adaptador do titular (todos os runs não fixados por escolha);
//   - alias conhecido ⇒ o adaptador desse alias;
//   - alias DESCONHECIDO ⇒ [ErrModelNotServed]. Nunca o titular: um run fixado num modelo que este
//     nó não serve não é servido por outro.
//
// Um run fixado no candidato cujo contexto perdesse o alias cairia no adaptador do titular — e
// seria recusado aí pela conferência do perfil fixado ([modelgateway.RouteProfileSet.CheckPinnedProfile]):
// o digest fixado no run é o do perfil do candidato, não o do titular.
type clientePorModelo struct {
	titular  string
	porAlias map[string]agentruntime.ModelClient
	// series conta cada pedido pelo alias ESCOLHIDO AQUI (AOS-520, PR 2). nil ⇒ sem candidato a
	// servir: não se conta nada.
	series *seriesPorModelo
	// aoTurno, quando ligado, recebe o resultado de cada pedido com o alias escolhido (AOS-521): é
	// por onde o disjuntor do candidato vê um turno com o modelo servido diferente. Ligado no
	// arranque, e só com o disjuntor em `observe` ou `enforce`; por ligar — a omissão — nada corre.
	aoTurno atomic.Pointer[func(ctx context.Context, alias string, resp agentruntime.ModelResponse, err error)]
}

// Compile-time: o despachante é um cliente de modelo.
var _ agentruntime.ModelClient = (*clientePorModelo)(nil)

// Call implementa [agentruntime.ModelClient].
func (c *clientePorModelo) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	alias := modeloDoRunFrom(ctx)
	if alias == "" {
		alias = c.titular
	}
	cliente, ok := c.porAlias[alias]
	if !ok {
		return agentruntime.ModelResponse{}, fmt.Errorf("%w: causa=%s", ErrModelNotServed, modelgateway.PinnedCauseModelNotServed)
	}
	resp, err := cliente.Call(ctx, view)
	c.series.observarTurno(alias, resp, err)
	if f := c.aoTurno.Load(); f != nil {
		(*f)(ctx, alias, resp, err)
	}
	return resp, err
}

// aliasServido diz se este nó tem como servir um run fixado no alias dado: o do titular, ou o de
// um perfil assinado carregado. É a pergunta que a retoma faz ANTES de re-hospedar um run fixado
// por escolha, para um run que o despachante recusaria não chegar a ser re-hospedado e a falhar no
// primeiro turno vivo.
func (n *Node) aliasServido(alias string) bool {
	if n == nil || alias == "" {
		return false
	}
	if alias == n.modelID {
		return true
	}
	for _, e := range n.perfisDaRota.naoTitulares(n.modelID) {
		if e.Profile.Requested == alias {
			return true
		}
	}
	return false
}
