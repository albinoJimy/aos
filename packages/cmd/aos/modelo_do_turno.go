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
