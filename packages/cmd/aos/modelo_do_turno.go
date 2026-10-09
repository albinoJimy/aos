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
	if goal.ProjectionVersion != "" {
		return goal
	}
	if perfil, ok := modelgateway.RouteProfileFor(goal.Model.ModelID); ok {
		goal.ProjectionVersion = perfil.ProjectionVersion
	}
	return goal
}

// versaoDaProjeccaoParaORegisto devolve o que o registo de retoma grava como versão da projecção
// do run: a versão fixada, ou vazio quando o run não tem nenhuma — incluindo o run retomado que
// traz a marca [agentruntime.ProjectionVersionUnpinned], para que a re-escrita do registo na
// retoma tenha os bytes do registo original.
func versaoDaProjeccaoParaORegisto(goal agentruntime.Goal) string {
	return agentruntime.NormalizeProjectionVersion(goal.ProjectionVersion)
}
