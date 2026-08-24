package main

import (
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// PRODUTOR ESTRUTURAL vs JANELA VAZIA — as duas coisas que o rótulo `avaliavel` confundia.
//
// O achado D da verificação de funcionamento de 2026-08-23. O rótulo era calculado de
// `Samples > 0` na janela CORRENTE (5 min), e o texto de HELP prometia uma propriedade
// ESTRUTURAL: «o SLI nao tem produtor neste no e a regra NUNCA pode disparar».
//
// São coisas diferentes, e a diferença tem consequência. Das doze regras, QUATRO ficavam mal
// rotuladas num nó sem tráfego — três delas `critical`:
//
//	mediation_overhead_high, mediation_overhead_p95_high, cost_per_trajectory_high, override_rate_high
//
// A assimetria é a pior possível: uma avaria que PÁRA o tráfego — mediação degradada ao ponto
// de os chamadores desistirem, o Reference Monitor a falhar antes de o span fechar — esvazia a
// janela e faz o CRITICAL auto-declarar-se «NUNCA pode disparar» exactamente quando o operador
// precisa dele. E a cada REINÍCIO do nó todas as regras derivadas de spans nascem assim, até à
// primeira tool call.
//
// A informação para separar os dois estados já existia no processo: [sloEvaluatorBanner]
// declara-a em prosa no arranque («LIGADOS pela torneira» vs «DORMENTES»), mas em binário
// grosseiro e num sítio que não chega ao `/metrics`. Aqui fica por-SLI e legível por máquina.
//
// # PORQUE UM RÓTULO NOVO E NÃO OUTRO VALOR NO ANTIGO
//
// `avaliavel` mantém o significado que já tinha — «esta passagem avaliou a regra» — e ganha ao
// lado `produtor`, que é o facto estrutural. Um terceiro valor no rótulo antigo mudaria o
// significado de séries já emitidas; um rótulo novo é ADITIVO. As três leituras:
//
//	produtor="0"               ⇒ este binário não compõe quem alimenta o SLI. A regra NUNCA dispara.
//	produtor="1" avaliavel="0" ⇒ o produtor existe e a janela está vazia. A regra PODE disparar.
//	produtor="1" avaliavel="1" ⇒ avaliada nesta passagem.
//
// # A CLASSIFICAÇÃO, e de onde vem cada linha
//
//   - SONDAS LOCAIS por passagem — sempre presentes, independentes de tráfego. É por isso que
//     são as duas únicas que ficam estáveis a `avaliavel="1"` num nó ocioso.
//   - DERIVADOS DOS SPANS DO PRÓPRIO NÓ — o produtor é este binário, e existe se a torneira
//     estiver composta: `execute_tool` (o ponto ÚNICO de mediação), `chat` e `aos.decision`.
//   - SEM PRODUTOR NESTE BINÁRIO — headroom e fidelidade de replay ficam explicitamente a `nil`
//     no avaliador («o nó não tem produtor para eles»); cache-hit-rate e cold-start entrariam
//     se o Model Gateway ou o pool de sandbox partilhassem o tracer, e nenhum dos dois é
//     composto aqui — o recorder do cache-hit-rate não tem UM ÚNICO call site de produção.
//
// Declarar `produtor="1"` para estes últimos por "poderem" ter produtor noutro destacamento
// seria a promessa a mais que este eixo existe para evitar: o rótulo descreve ESTE binário.
func produtorNoNo(sli string, torneiraLigada bool) bool {
	switch sli {
	case otelgenai.SLIControlPlaneAvailability, otelgenai.SLIAuditWORMIntegrity:
		return true
	case otelgenai.SLIMediationOverheadP95, otelgenai.SLICostPerTrajectory, otelgenai.SLIOverrideRate:
		return torneiraLigada
	default:
		// cache_hit_rate, sandbox_cold_start_p95, headroom_tokens, replay_fidelity — e
		// qualquer SLI que venha a ser acrescentado ao catálogo sem produtor cablado aqui.
		// O default é `false` de propósito: um SLI novo nasce declarado como SEM produtor
		// até alguém o ligar, que é a direcção segura — o inverso anunciaria uma regra viva
		// sobre um sinal que ninguém alimenta.
		return false
	}
}
