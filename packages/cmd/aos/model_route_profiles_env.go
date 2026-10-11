package main

// OS PERFIS DE ROTA ASSINADOS (AOS-519, emenda ao ADR-036 §2.8) — `AOS_MODEL_ROUTE_PROFILES_DIR` e
// `AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR`.
//
// Até aqui o perfil de uma rota — que modelo o proxy deve declarar ter servido, com que
// parâmetros e em que versão da projecção — era só uma tabela em código: um modelo novo custava
// um PR e uma imagem nova do nó. Com a pasta configurada, o nó aceita perfis vindos de fora do
// binário, por REGISTOS DE ENTRADA ASSINADOS pelo dono, cada um a citar o veredicto do banco de
// ensaio para esse perfil (pacote `policy/routeentry` do Model Gateway, onde está o formato, o que
// a assinatura cobre e cada conferência).
//
// INERTE POR OMISSÃO. Sem `AOS_MODEL_ROUTE_PROFILES_DIR` este ficheiro não faz nada: o conjunto
// de perfis é a tabela em código (o ponteiro nil), o gateway não recebe candidatos, o cliente de
// modelo não ganha invólucro e o arranque não diz linha nenhuma. A âncora sozinha, sem a pasta,
// não é lida.
//
// FAIL-CLOSED. Com a pasta configurada, ou TODOS os registos dela passam em TODAS as
// conferências, ou o nó não arranca ([ErrBadModelRouteProfiles], com a causa em vocabulário
// fechado e o rótulo do registo — nunca conteúdo dos ficheiros). Não há «carrega o que conseguir».
// Uma pasta vazia é legítima: zero perfis assinados, e o nó é o de sem pasta.
//
// PRECEDÊNCIA. Um perfil assinado para um alias que a tabela em código já conhece SUBSTITUI essa
// entrada para o nó inteiro (decisão D1 do dono), e o arranque di-lo, com o modelo esperado e o
// digest de antes e de agora. Um perfil para um alias que a tabela não conhece acrescenta-se. Em
// nenhum dos casos o perfil autoriza o nome: isso é da allowlist assinada, conferida aqui.
//
// SÓ NO ARRANQUE. A pasta lê-se uma vez, antes de o gateway existir. Mudar, acrescentar ou apagar
// um ficheiro com o nó a correr não tem efeito até o nó ser recriado — de propósito: um perfil que
// muda a meio falha fechado os runs em curso (ADR-040 §2.11). Trocar um perfil é recriar o nó, com
// a rota drenada.
//
// A FATIA do canary que o registo traz é lida, validada e mostrada; não tem efeito nesta versão.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// ErrBadModelRouteProfiles — a pasta de perfis de rota assinados está configurada e alguma coisa
// nela não passa. Fail-closed: o nó não arranca. A causa vem a seguir, em vocabulário fechado
// ([routeentry.Causes]), com o rótulo do registo.
var ErrBadModelRouteProfiles = errors.New("aos: perfis de rota assinados recusados — AOS_MODEL_ROUTE_PROFILES_DIR exige AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR (base64 da chave publica ed25519 de quem assina os perfis, dada por fora e so para perfis) e TODOS os registos da pasta conferidos: assinatura, registo, ambiente, imagem do proxy, relatorio de qualificacao citado e allowlist. Nenhum perfil e carregado em parte; a causa e o registo vem a seguir")

// maxSeriesDaRotaDoModelo é o MÁXIMO de séries de `aos_model_route_checks_total`: os três
// resultados vezes o maior conjunto possível de valores do modelo servido — os modelos esperados
// das quatro entradas da tabela em código, mais um por registo assinado (no máximo
// [routeentry.MaxEntries]), mais `outro` e `nao_reportado`.
const maxSeriesDaRotaDoModelo = 3 * (4 + routeentry.MaxEntries + 2)

// perfisAssinados é o que o nó carregou da pasta, no arranque. Um ponteiro nil é «sem pasta».
type perfisAssinados struct {
	// entradas são os registos conferidos, pela ordem do rótulo. Vazio com a pasta vazia.
	entradas []routeentry.Entry
	// conjunto é a tabela em código com os perfis assinados por cima; nil quando não há nenhum.
	conjunto *modelgateway.RouteProfileSet
	// externa é a allowlist externa com que os registos foram conferidos (nil ⇒ a embebida). É a
	// MESMA que o gateway recebe: a que autorizou os nomes é a que os vai julgar em cada pedido.
	externa *allowlist.Policy
}

// perfis devolve o conjunto de perfis do nó: a tabela em código (nil) sem pasta ou sem registos.
func (p *perfisAssinados) perfis() *modelgateway.RouteProfileSet {
	if p == nil {
		return nil
	}
	return p.conjunto
}

// candidatos devolve os perfis assinados, para o gateway ([modelgateway.ProductionConfig.RouteProfiles]).
func (p *perfisAssinados) candidatos() []modelgateway.RouteProfile {
	if p == nil {
		return nil
	}
	out := make([]modelgateway.RouteProfile, 0, len(p.entradas))
	for _, e := range p.entradas {
		out = append(out, e.Profile)
	}
	return out
}

// registos devolve o que o gateway sela de cada perfil carregado.
func (p *perfisAssinados) registos() []modelgateway.RouteProfileEntry {
	if p == nil {
		return nil
	}
	out := make([]modelgateway.RouteProfileEntry, 0, len(p.entradas))
	for _, e := range p.entradas {
		out = append(out, modelgateway.RouteProfileEntry{
			Requested: e.Profile.Requested, ProfileDigest: e.ProfileDigest, EntryDigest: e.EntryDigest, ReportDigest: e.ReportDigest,
		})
	}
	return out
}

// modelRegionBoardFromEnv lê a fronteira de soberania que o nó declara ao gateway
// (`AOS_MODEL_REGION` e `AOS_MODEL_BOARD`), com as omissões que casam com a allowlist embebida.
func modelRegionBoardFromEnv() (region, board string) {
	region = defaultModelGatewayRegion
	if v := strings.TrimSpace(os.Getenv("AOS_MODEL_REGION")); v != "" {
		region = v
	}
	board = defaultModelGatewayBoard
	if v := strings.TrimSpace(os.Getenv("AOS_MODEL_BOARD")); v != "" {
		board = v
	}
	return region, board
}

// loadSignedRouteProfilesFromEnv lê a pasta de perfis assinados, se estiver configurada. Vazia ⇒
// (nil, nil): sem pasta, nada é lido — nem a âncora, nem a allowlist.
//
// Com a pasta, confere os registos contra a allowlist EM VIGOR no nó — a externa, se montada, ou a
// embebida — para o board e a região do nó, e contra a âncora dos perfis, que não pode ser a de
// nenhuma das duas allowlists.
func loadSignedRouteProfilesFromEnv() (*perfisAssinados, error) {
	dir := strings.TrimSpace(os.Getenv("AOS_MODEL_ROUTE_PROFILES_DIR"))
	if dir == "" {
		return nil, nil
	}
	region, board := modelRegionBoardFromEnv()
	externa, err := loadModelAllowlistFromEnv()
	if err != nil {
		return nil, err
	}
	emVigor := externa
	if emVigor == nil {
		if emVigor, err = allowlist.LoadPolicy(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadModelAllowlist, err)
		}
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{
		Dir:         dir,
		TrustAnchor: os.Getenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR"),
		// As duas âncoras não são intermutáveis: a dos perfis não pode ser a da allowlist externa
		// nem a da embebida.
		ForeignAnchors: []string{os.Getenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR"), allowlist.EmbeddedTrustAnchor()},
		Board:          board,
		Region:         region,
		Allowlist:      emVigor,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadModelRouteProfiles, err)
	}
	p := &perfisAssinados{entradas: entradas, externa: externa}
	if len(entradas) > 0 {
		// A mesma validação que o gateway faz ao compor-se, feita já: um conjunto que não se
		// compõe aborta aqui, com o erro do gateway.
		if p.conjunto, err = modelgateway.NewRouteProfileSet(p.candidatos()...); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadModelRouteProfiles, err)
		}
	}
	return p, nil
}

// modelAllowlistDoNo devolve a allowlist externa que o gateway do nó recebe (nil ⇒ a embebida).
// Sem perfis assinados lê-a do ambiente, como sempre; com eles é a que eles já leram.
func modelAllowlistDoNo(assinados *perfisAssinados) (*allowlist.Policy, error) {
	if assinados != nil {
		return assinados.externa, nil
	}
	return loadModelAllowlistFromEnv()
}

// modelRouteProfilesBanner declara os perfis assinados. Sem pasta não sai linha nenhuma: o
// arranque de um nó que não a configura é o de antes. Só leva identificadores e digests.
func modelRouteProfilesBanner(p *perfisAssinados) []string {
	if p == nil {
		// A pasta está declarada e NÃO foi lida: o nó não compôs o gateway de modelo por ambiente
		// (sem AOS_MODEL_ENDPOINT, ou com um cliente injectado). Nenhum perfil está em vigor, e
		// isso não fica em silêncio. Sem a variável não sai linha nenhuma.
		if strings.TrimSpace(os.Getenv("AOS_MODEL_ROUTE_PROFILES_DIR")) != "" {
			return []string{"AVISO (EPIC-06/AOS-519): AOS_MODEL_ROUTE_PROFILES_DIR esta definida e a pasta NAO foi lida nem conferida — o no nao compos o gateway de modelo a partir do ambiente (AOS_MODEL_ENDPOINT por definir). Nenhum perfil de rota assinado esta em vigor"}
		}
		return nil
	}
	if len(p.entradas) == 0 {
		return []string{"perfis de rota assinados (EPIC-06/AOS-519): AOS_MODEL_ROUTE_PROFILES_DIR definida e a pasta esta vazia — zero perfis assinados; o no usa a tabela de perfis em codigo, como sem a pasta. A pasta so e lida no arranque"}
	}
	linhas := make([]string, 0, len(p.entradas))
	for _, e := range p.entradas {
		origem := "ACRESCENTA-SE a tabela de perfis em codigo, que nao tem este alias"
		if antes, tem := modelgateway.RouteProfileFor(e.Profile.Requested); tem {
			origem = fmt.Sprintf("SUBSTITUI a entrada da tabela de perfis em codigo para este alias, para o no inteiro: modelo esperado de antes %q (digest %s)", antes.ExpectedModel, antes.Digest())
		}
		linhas = append(linhas, fmt.Sprintf("perfil de rota ASSINADO carregado (EPIC-06/AOS-519): registo %q, alias %q — %s; modelo esperado de agora %q (digest do perfil %s). Digest do registo assinado %s; digest do relatorio de qualificacao citado %s (veredicto qualificado, do modo real, para este perfil e para a imagem do proxy declarada). Fatia inicial do canary: %d%% — lida e validada, SEM EFEITO nesta versao (AOS-520). O perfil NAO autoriza o nome: a allowlist assinada autoriza-o para o board e a regiao deste no, conferido no arranque. Carregado so no arranque: mudar a pasta com o no a correr nao tem efeito ate o no ser recriado. A assinatura prova que o dono assinou este registo, nao que o banco de ensaio correu",
			e.Label, e.Profile.Requested, origem, e.Profile.ExpectedModel, e.ProfileDigest, e.EntryDigest, e.ReportDigest, e.InitialSharePercent))
	}
	return linhas
}

// clienteComPerfis é o cliente de modelo do nó com os perfis assinados à mão. Não muda nenhuma
// chamada: existe para o [Bootstrap] e o arranque os encontrarem no cliente que recebem
// ([perfisDoCliente]), sem um canal lateral e SEM VOLTAR A LER A PASTA. Só existe com a pasta
// configurada: sem ela o cliente é o de sempre, sem este invólucro.
type clienteComPerfis struct {
	inner  agentruntime.ModelClient
	perfis *perfisAssinados
}

// Call implementa [agentruntime.ModelClient], sem tocar em nada.
func (c clienteComPerfis) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return c.inner.Call(ctx, view)
}

func (c clienteComPerfis) perfisAssinadosDoNo() *perfisAssinados { return c.perfis }

// Os contadores dos invólucros de dentro passam adiante, como em [clienteComEstado].
func (c clienteComPerfis) rotaDoModelo() *contadoresDaRota     { return rotaDoCliente(c.inner) }
func (c clienteComPerfis) formaDaResposta() *contadoresDaForma { return formaDoCliente(c.inner) }
func (c clienteComPerfis) respostasRecusadas() *contadoresDeRejeicao {
	return rejeicoesDoCliente(c.inner)
}
func (c clienteComPerfis) estadoDoProvider() *contadoresDoEstado { return estadoDoCliente(c.inner) }

// fonteDosPerfis é o que um cliente de modelo expõe para os perfis assinados serem encontrados.
type fonteDosPerfis interface {
	perfisAssinadosDoNo() *perfisAssinados
}

// perfisDoCliente devolve os perfis assinados do cliente de modelo dado, ou nil — sem pasta, com o
// modelo de referência, ou com um cliente injectado.
func perfisDoCliente(c agentruntime.ModelClient) *perfisAssinados {
	if f, ok := c.(fonteDosPerfis); ok {
		return f.perfisAssinadosDoNo()
	}
	return nil
}
