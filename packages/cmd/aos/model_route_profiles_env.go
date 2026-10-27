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
// um ficheiro com o nó a correr não tem efeito até o nó ser recriado. Trocar um perfil é recriar o
// nó, COM A ROTA DRENADA — e a drenagem é uma regra OPERACIONAL, que o nó não impõe.
//
// O PERFIL FICA FIXADO POR RUN (AOS-520). Com perfis assinados carregados, cada run novo fica
// fixado, pelo digest, no perfil em vigor para o seu alias ([fixarPerfilCom]); o digest vai para o
// registo de retoma e para o manifesto de cada turno. Um run começado com o perfil A e retomado
// num nó que arrancou com o perfil B — ou sem perfil para o alias, ou sem a pasta — NÃO continua:
// a retoma pela API recusa antes de re-hospedar, a varredura de crash-resume deixa-o como órfão
// declarado, e o adaptador do gateway recusa o turno antes de qualquer pedido, com a causa em
// vocabulário fechado ([modelgateway.PinnedRouteProfileCauses]). Era o «limite conhecido» do
// AOS-519. A drenagem deixa de ser a única protecção, e continua a ser o procedimento recomendado.
//
// O QUE CONTINUA ABERTO. Um run SEM perfil fixado — de um nó sem a pasta, ou de antes do AOS-520 —
// não é fixado a meio e continua como sempre: a troca da tabela em código entre duas imagens do
// nó, sem pasta de perfis, não é detectada.
//
// A FATIA do canary que o registo traz é lida, validada e mostrada. Só tem efeito no registo do
// CANDIDATO que o nó serve — um registo de uso `run` para um alias que não é o do titular, com
// `AOS_MODEL_CANDIDATE=on` e a fatia maior que zero (AOS-520, ADR-041; `modelo_candidato.go`).
//
// O USO (AOS-530, ADR-042 §2.3). Um registo de uso `tool` é o perfil de um alias que só uma tool
// chama: entra no conjunto de perfis do gateway como os outros, e nunca é candidato
// ([perfisAssinados.naoTitulares]); a fatia dele é 0 e o relatório citado é da experiência da
// media do perfil — `ocr` para `imagem`, `video` para `video` (AOS-536) — (conferidos na leitura da
// pasta), e não pode ser do alias do titular ([perfisAssinados.conferirUsoTool]).

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

// ErrModelRouteProfilesWithoutDurableAudit — AOS_MODE=production, há perfis de rota assinados
// carregados, e o audit de governação do gateway não é durável (AOS_MODEL_AUDIT_PATH por definir).
// Fail-closed: o selo «perfil carregado» é a prova de que perfil serviu que nome e desde quando, e
// num audit em memória desaparecia no reinício seguinte.
var ErrModelRouteProfilesWithoutDurableAudit = errors.New("aos: perfis de rota assinados em AOS_MODE=production exigem o audit de governacao do gateway DURAVEL — defina AOS_MODEL_AUDIT_PATH; sem ele o selo de cada perfil carregado ficava em memoria e perdia-se no reinicio (fail-closed)")

// ErrModelRouteProfilesWithoutGateway — AOS_MODE=production, AOS_MODEL_ROUTE_PROFILES_DIR está
// definida e o nó não tem gateway de modelo (AOS_MODEL_ENDPOINT por definir). Fail-closed: a pasta
// não seria lida nem conferida, e o nó correria com o modelo de referência enquanto a configuração
// diz que há perfis assinados.
var ErrModelRouteProfilesWithoutGateway = errors.New("aos: AOS_MODEL_ROUTE_PROFILES_DIR definida em AOS_MODE=production sem gateway de modelo (AOS_MODEL_ENDPOINT por definir) — a pasta nao seria lida nem conferida e nenhum perfil assinado ficaria em vigor; defina AOS_MODEL_ENDPOINT ou retire a variavel (fail-closed)")

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
	// candidatoLigado é o interruptor `AOS_MODEL_CANDIDATE=on` (AOS-520), lido uma vez no arranque.
	// false — a omissão — ⇒ nenhum candidato é servido, haja ou não um registo não-titular.
	candidatoLigado bool
	// selarFimDoCanary GARANTE o selo do fim do canary no audit de governação do MESMO gateway que
	// serve os modelos (AOS-520): sela se o audit ainda não o tiver ([seladorDoFimDoCanary]).
	// Ligado na composição do gateway; nil ⇒ não há onde selar.
	selarFimDoCanary func(context.Context, fimDoCanary) error
	// selosDoFimDoCanary conta, nesse mesmo audit, os selos do fim do canary de um registo de
	// entrada. nil ⇒ não há onde ler.
	selosDoFimDoCanary func(context.Context, string) (int, error)
	// porModelo são as séries do `/metrics` por modelo (AOS-520, PR 2). Criadas na composição do
	// gateway, e SÓ com um candidato a servir; nil ⇒ as três famílias não existem.
	porModelo *seriesPorModelo
	// disjuntor é o modo de `AOS_MODEL_BREAKER` (AOS-521), lido uma vez no arranque: `observe` ou
	// `enforce`. Vazio — a omissão — é `off`.
	disjuntor string
	// despachante é o cliente de modelo por alias do nó, quando existe (um perfil assinado
	// não-titular carregado). É por ele que o disjuntor LIGADO vê o resultado de cada pedido.
	despachante *clientePorModelo
	// deModelo são TODAS as tools de modelo compostas, pela ordem do registo de tools (AOS-566):
	// uma por entrada com bloco `modelo`, cada uma da classe que declarou. É esta lista que o nó
	// liga ao gateway, regista no RM, custeia e fecha — sem saber quantas são nem como se chamam.
	deModelo []toolDeModelo
	// ocr é a ligação da classe IMAGEM (a `ocr_read` do manifesto de produção), quando composta: a
	// tool chama o alias de um registo de uso `tool` desta pasta, pelo MESMO gateway. nil ⇒ sem
	// nenhuma. O gateway liga-se a todas na composição ([newGatewayModelClientComPerfis]).
	//
	// É A PRIMEIRA DA CLASSE, e serve a série `aos_ocr_read_total` do `/metrics` — a única parte
	// deste caminho que continua a ser por classe fixa. Um nó com DUAS tools de imagem corre as
	// duas, conta cada uma nos seus contadores, e publica a família da primeira; publicar uma
	// família por tool composta é mudança do `/metrics`, fora desta fatia.
	ocr *ligacaoOCR
	// video é a ligação da classe VÍDEO (a `video_read`), pela mesma regra da `ocr`, para a série
	// `aos_video_read_total`.
	video *ligacaoVideo
}

// juntarToolDeModelo guarda uma tool de modelo composta: na lista que o nó percorre, e — se for a
// primeira da classe dela — no campo por onde o `/metrics` publica a família da classe.
func (p *perfisAssinados) juntarToolDeModelo(t toolDeModelo) {
	p.deModelo = append(p.deModelo, t)
	switch l := t.(type) {
	case *ligacaoOCR:
		if p.ocr == nil {
			p.ocr = l
		}
	case *ligacaoVideo:
		if p.video == nil {
			p.video = l
		}
	}
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
			return []string{"AVISO (EPIC-06/AOS-519): AOS_MODEL_ROUTE_PROFILES_DIR esta definida e a pasta NAO foi lida nem conferida — o no nao compos o gateway de modelo a partir do ambiente (AOS_MODEL_ENDPOINT por definir). Nenhum perfil de rota assinado esta em vigor. Em AOS_MODE=production isto aborta o arranque"}
		}
		// A âncora sem a pasta: não é lida, e quem a definiu pode julgar que há perfis em vigor.
		if strings.TrimSpace(os.Getenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR")) != "" {
			return []string{"AVISO (EPIC-06/AOS-519): AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR esta definida e AOS_MODEL_ROUTE_PROFILES_DIR nao — a ancora NAO e lida e nenhum perfil de rota assinado esta em vigor: o no usa a tabela de perfis em codigo. Defina a pasta, ou retire a ancora"}
		}
		return nil
	}
	if len(p.entradas) == 0 {
		return []string{"perfis de rota assinados (EPIC-06/AOS-519): AOS_MODEL_ROUTE_PROFILES_DIR definida e a pasta esta vazia — zero perfis assinados; o no usa a tabela de perfis em codigo, como sem a pasta. A pasta so e lida no arranque"}
	}
	// AOS-520: a fatia do registo do candidato que este nó SERVE tem efeito, e a linha di-lo. Em
	// todos os outros casos — sem candidato a servir — a linha é, byte a byte, a de antes.
	candidato, serve := p.candidatoAServir(modelNameFromEnv())
	linhas := make([]string, 0, len(p.entradas))
	for _, e := range p.entradas {
		fatia := fmt.Sprintf("%d%% — lida e validada, SEM EFEITO nesta versao (AOS-520)", e.InitialSharePercent)
		if serve && e.EntryDigest == candidato.entryDigest {
			fatia = fmt.Sprintf("%d%% — EM VIGOR: este registo e o do candidato que o no serve (AOS-520, ADR-041; ver a linha do modelo candidato)", e.InitialSharePercent)
		}
		if e.ForTool() {
			// AOS-530: um registo de uso `tool` diz-se à parte, e a linha dos de uso `run` fica, byte
			// a byte, a de antes. AOS-536: a experiência citada é a da media do perfil; a linha de um
			// registo de imagem fica, byte a byte, a de antes.
			experiencia := "experiencia ocr"
			if e.Media == modelgateway.CapabilityVideo {
				experiencia = "experiencia video, perfil de video (AOS-536)"
			}
			linhas = append(linhas, fmt.Sprintf("perfil de rota ASSINADO de USO TOOL carregado (EPIC-06/AOS-530, ADR-042): registo %q, alias %q — o perfil de um alias que so uma tool chama; NUNCA e candidato: nao serve runs, nao tem adaptador de run, nao entra no canary nem no disjuntor, e um run fixado neste alias e recusado. Modelo esperado %q (digest do perfil %s). Digest do registo assinado %s; digest do relatorio citado %s (%s, veredicto qualificado, do modo real, para este perfil e para a imagem do proxy declarada). Fatia 0 (um registo de uso tool nao tem canary). O perfil NAO autoriza o nome: a allowlist assinada autoriza-o para o board e a regiao deste no, conferido no arranque. Carregado so no arranque",
				e.Label, e.Profile.Requested, e.Profile.ExpectedModel, e.ProfileDigest, e.EntryDigest, e.ReportDigest, experiencia))
			continue
		}
		origem := "ACRESCENTA-SE a tabela de perfis em codigo, que nao tem este alias"
		if antes, tem := modelgateway.RouteProfileFor(e.Profile.Requested); tem {
			origem = fmt.Sprintf("SUBSTITUI a entrada da tabela de perfis em codigo para este alias, para o no inteiro: modelo esperado de antes %q (digest %s)", antes.ExpectedModel, antes.Digest())
		}
		linhas = append(linhas, fmt.Sprintf("perfil de rota ASSINADO carregado (EPIC-06/AOS-519): registo %q, alias %q — %s; modelo esperado de agora %q (digest do perfil %s). Digest do registo assinado %s; digest do relatorio de qualificacao citado %s (veredicto qualificado, do modo real, para este perfil e para a imagem do proxy declarada). Fatia inicial do canary: %s. O perfil NAO autoriza o nome: a allowlist assinada autoriza-o para o board e a regiao deste no, conferido no arranque. Carregado so no arranque: mudar a pasta com o no a correr nao tem efeito ate o no ser recriado. PERFIL FIXADO POR RUN (AOS-520): com perfis assinados carregados, cada run novo fica fixado pelo digest no perfil em vigor para o seu alias; retomado num no com outro perfil para o alias, ou sem nenhum, nao continua (retoma recusada, ou orfao declarado no crash-resume). A assinatura prova que o dono assinou este registo, nao que o banco de ensaio correu",
			e.Label, e.Profile.Requested, origem, e.Profile.ExpectedModel, e.ProfileDigest, e.EntryDigest, e.ReportDigest, fatia))
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
