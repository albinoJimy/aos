package main

// modelo_do_turno.go — O MODELO PEDIDO ENTRA NO GOAL (AOS-396).
//
// Medido em produção a 2026-09-15: todos os `turn.recorded` tinham `manifest.model.model_id`
// vazio, embora o gateway tivesse selado cada chamada com o modelo. O runtime grava o
// `Goal.Model.ModelID`, e o nó nunca o preenchia — o `submitRequest` não tem modelo e o nome
// (`AOS_MODEL_NAME`) só chegava ao adaptador do gateway. O mesmo valor vazio ia para o span
// `chat` (`gen_ai.request.model`) e para a admissão do turno, e desligava a verificação de
// modelo do replay, que só compara quando há modelo.
//
// A correcção fica numa só via: [NodeService.hostRun] passa o Goal por [Node.fixarModelo]
// antes do registo de crash-resume e do primeiro turno, e a submissão, a retoma e a varredura
// de crash-resume passam todas por ali.

import (
	"os"
	"strings"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// modelNameFromEnv é a ÚNICA leitura de `AOS_MODEL_NAME`: o nome que o adaptador do gateway
// pede ([parseModelFromEnv]) e o que o nó declara no manifesto ([Config.ModelID]) saem daqui,
// para não poderem divergir.
func modelNameFromEnv() string {
	return strings.TrimSpace(os.Getenv("AOS_MODEL_NAME"))
}

// fixarModelo escreve no Goal o modelo que o nó pede.
//
//   - Gateway por ambiente (modelo AUTORITATIVO): sobrepõe-se ao que o goal trouxer, porque o
//     adaptador envia sempre o seu modelo. Cobre a retoma de um run gravado com outra
//     configuração: o manifesto dos turnos novos diz o modelo que de facto viajou.
//   - Modelo de referência: preenche só um goal sem modelo; um embedder ou teste que declare o
//     seu fica como está.
//   - Config.Model injectado sem Config.ModelID: não declara nada.
//
// `Params` e `Seed` NÃO são tocados: nenhum cliente composto pelo nó os envia ao provider, e o
// manifesto não pode afirmar parâmetros que não viajaram.
func (n *Node) fixarModelo(goal agentruntime.Goal) agentruntime.Goal {
	if n == nil || n.modelID == "" {
		return goal
	}
	if n.modeloAutoritativo || goal.Model.ModelID == "" {
		goal.Model.ModelID = n.modelID
	}
	return goal
}

// fixarProjeccao escreve no Goal a versão da projecção nativa em que o run fica FIXADO (AOS-513):
// a que o perfil da rota do modelo do run declara ([modelgateway.RouteProfile.ProjectionVersion]).
//
// Um Goal sem versão é um run NOVO. Se o perfil da sua rota não declara versão — o caso de todos
// os perfis de hoje — o Goal fica como está: sem versão fixada, a projecção de cada turno é a do
// interruptor do nó (`AOS_MODEL_PROJECTION_VERSION`), e o registo de retoma grava os bytes de
// sempre. Um Goal que já traz versão, ou a marca [agentruntime.ProjectionVersionUnpinned] de um
// run retomado que começou sem nenhuma, não é tocado: a projecção de um run não muda a meio,
// mesmo que a imagem do nó tenha mudado o perfil entretanto.
func fixarProjeccao(goal agentruntime.Goal) agentruntime.Goal {
	return fixarProjeccaoCom(goal, nil)
}

// fixarProjeccaoCom é [fixarProjeccao] sobre o conjunto de perfis do nó (AOS-519): com perfis
// assinados, a versão é a do perfil ASSINADO do alias. nil ⇒ a tabela em código, como sempre.
func fixarProjeccaoCom(goal agentruntime.Goal, perfis *modelgateway.RouteProfileSet) agentruntime.Goal {
	if goal.ProjectionVersion != "" {
		return goal
	}
	if perfil, ok := perfis.For(goal.Model.ModelID); ok {
		goal.ProjectionVersion = perfil.ProjectionVersion
	}
	return goal
}

// fixarProjeccaoDoRun fixa no Goal a versão da projecção do perfil da rota, lido do conjunto de
// perfis DESTE nó: a tabela em código, com os perfis assinados que o nó carregou no arranque por
// cima (AOS-519). É a que a hospedagem de um run usa.
func (n *Node) fixarProjeccaoDoRun(goal agentruntime.Goal) agentruntime.Goal {
	if n == nil {
		return fixarProjeccao(goal)
	}
	return fixarProjeccaoCom(goal, n.perfisDaRota.perfis())
}

// versaoDaProjeccaoParaORegisto devolve o que o registo de retoma grava como versão da projecção
// do run: a versão fixada, ou vazio quando o run não tem nenhuma — incluindo o run retomado que
// traz a marca [agentruntime.ProjectionVersionUnpinned], para que a re-escrita do registo na
// retoma tenha os bytes do registo original.
func versaoDaProjeccaoParaORegisto(goal agentruntime.Goal) string {
	return agentruntime.NormalizeProjectionVersion(goal.ProjectionVersion)
}

// fixarPerfilCom escreve no Goal o digest do PERFIL DA ROTA em que o run fica FIXADO (AOS-520): o
// do perfil que o nó tem em vigor para o modelo do run.
//
// SÓ UM NÓ COM PERFIS ASSINADOS FIXA (decisão do dono, 2026-10-11). Sem a pasta de perfis, ou com
// ela vazia, `assinados` não tem conjunto e o Goal fica como está: o nó é o de antes, e o registo
// de retoma grava os bytes de sempre. Com perfis assinados carregados, um run NOVO — o Goal sem
// digest — fica fixado no perfil em vigor para o seu alias, seja ele um dos assinados ou uma
// entrada da tabela em código que nenhum assinado substituiu. Um alias sem perfil não fixa nada.
//
// Um Goal que já traz digest, ou a marca [agentruntime.RouteProfileDigestUnpinned] de um run
// retomado que começou sem fixação, NÃO é tocado: o perfil de um run não se fixa nem se troca a
// meio. É o valor que o run traz — do seu registo de retoma — que é conferido com o perfil em
// vigor ([Node.conferirPerfilFixado], e o adaptador do gateway em cada turno); aqui nunca se
// recalcula.
func fixarPerfilCom(goal agentruntime.Goal, assinados *perfisAssinados) agentruntime.Goal {
	if goal.RouteProfileDigest != "" {
		return goal
	}
	conjunto := assinados.perfis()
	if conjunto == nil {
		return goal
	}
	if perfil, ok := conjunto.For(goal.Model.ModelID); ok {
		goal.RouteProfileDigest = perfil.Digest()
	}
	return goal
}

// fixarPerfilDoRun é [fixarPerfilCom] sobre os perfis assinados DESTE nó. É a que a hospedagem de
// um run usa, depois de [Node.fixarModelo] — o alias do run já é o que o nó pede.
func (n *Node) fixarPerfilDoRun(goal agentruntime.Goal) agentruntime.Goal {
	if n == nil {
		return goal
	}
	return fixarPerfilCom(goal, n.perfisDaRota)
}

// digestDoPerfilParaORegisto devolve o que o registo de retoma grava como perfil fixado do run: o
// digest, ou vazio quando o run não tem nenhum — incluindo o run retomado que traz a marca
// [agentruntime.RouteProfileDigestUnpinned], para que a re-escrita do registo na retoma tenha os
// bytes do registo original.
func digestDoPerfilParaORegisto(goal agentruntime.Goal) string {
	if goal.RouteProfileDigest == agentruntime.RouteProfileDigestUnpinned {
		return ""
	}
	return goal.RouteProfileDigest
}

// conferirPerfilFixado diz se o run do registo de retoma dado pode ser re-hospedado NESTE nó, pelo
// perfil de rota em que está fixado (AOS-520). nil ⇒ pode. Um erro é um
// [modelgateway.ErrPinnedRouteProfile] com a causa em vocabulário fechado
// ([modelgateway.PinnedRouteProfileCauses]).
//
// A DECISÃO É DO VALOR SELADO. O digest vem do registo de retoma — o que foi gravado quando o run
// começou —, e compara-se com o perfil que este nó tem em vigor para o alias com que o run seria
// re-hospedado ([Node.fixarModelo]: o modelo do nó, quando é o gateway que serve). É a MESMA
// conferência que o adaptador do gateway faz em cada turno
// ([modelgateway.RouteProfileSet.CheckPinnedProfile]); faz-se aqui, antes, para que um run que
// não pode seguir não chegue a ser re-hospedado — re-hospedado, falhava no primeiro turno vivo e
// ficava gravado como FALHADO, uma situação recuperável (repor o perfil) trocada por um terminal.
//
// Um registo sem digest — um run de antes da fixação, ou de um nó sem perfis assinados — segue,
// salvo o caso de [modelgateway.PinnedCauseSignedOverState].
func (n *Node) conferirPerfilFixado(rec integration.ResumeRecord) error {
	goal := n.fixarModelo(rec.GoalWith(""))
	var assinados *perfisAssinados
	if n != nil {
		assinados = n.perfisDaRota
	}
	return assinados.perfis().CheckPinnedProfile(goal.Model.ModelID, goal.RouteProfileDigest)
}

// causaDoPerfilFixado devolve a causa — de vocabulário fechado — de uma recusa pelo perfil fixado
// do run, ou vazio se err não for uma.
func causaDoPerfilFixado(err error) string {
	return modelgateway.PinnedRouteProfileCauseOf(err)
}
