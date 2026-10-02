package main

// streams_internos.go — O READ-PATH DOS RUNS SERVE RUNS, E ISSO TEM DE SER UM FACTO POSITIVO.
//
// # O DEFEITO QUE ISTO FECHA (AOS-426)
//
// O Event Store tem UM espaço de nomes de streams, e o `run_id` de um run É o seu stream. As
// rotas de leitura por-run endereçam esse espaço directamente a partir do URL:
//
//	GET /runs/{id}/trajectory   ->  Read(ctx, id, …) + Subscribe(Streams: [id])
//	GET /runs/{id}/reconstruct  ->  Read(ctx, id, 1)
//
// O padrão da stdlib casa `{id}` com UM segmento de caminho. Logo, qualquer stream interno do nó
// cujo nome não contenha uma barra é endereçável por estas rotas — e ambas serviam qualquer
// stream que EXISTISSE, porque a única guarda era o 404 no caso de o stream NÃO existir.
//
// Medido a 2026-09-21, com o gate soberano composto e um leitor autenticado de OUTRA região:
// treze streams internos respondiam `200` e serviam o seu conteúdo. Entre eles as aprovações
// four-eyes (`gov.approvals` — grants, pendentes e os registos de retoma, que carregam o
// `Goal`), as quatro classes de memória do nó, a identidade (`identity`), o registo de
// artefactos (`registry`), a posse de runs (`lease:<run>`) e — o que mais pesa — os nonces de
// ratificação e os challenges de quatro-olhos, que são primitivos de frescura e anti-replay de
// uma cerimónia HUMANA.
//
// Os oito streams do scheduler estavam seguros, e o discriminador era um só: **têm barra no
// nome**. Foi por acaso, não por desenho — a barra estava lá para namespacing, e a protecção
// veio de lambuja.
//
// # PORQUE É QUE A TRAVA É ESTA, E NÃO UMA LISTA DE NOMES PROIBIDOS
//
// Uma lista de nomes internos seria um conjunto ABERTO: o stream interno seguinte nasce
// servível, e ninguém é avisado. É o mesmo modo de falha que o `planos.go` fechou para as rotas
// («uma rota só existe se estiver registada») e que o AOS-417 voltou a encontrar.
//
// O que se usa é um FACTO POSITIVO, lido dos próprios dados: **num stream de run, os eventos
// declaram o run a que pertencem, e esse run é o stream.** Um stream interno não satisfaz isto,
// e não por convenção de nomes: porque os seus eventos pertencem a OUTRA coisa —
//
//   - `gov.approvals` grava com o `RunID` sintético `approval` (é a fila de aprovações, não um
//     run);
//   - `memory.semantic` grava com o `RunID` do run que ESCREVEU a memória, enquanto o stream é
//     a CLASSE de memória;
//   - `lease:<run>` grava a posse do run `<run>`, e o stream é `lease:<run>`, não `<run>`.
//
// Por isso a regra não precisa de saber que streams internos existem: eles auto-declaram-se
// quando o `RunID` dos seus eventos não coincide com o stream que se está a ler. Um stream
// interno NOVO fica coberto no dia em que nasce, sem ninguém se lembrar de nada.
//
// # O QUE ISTO CUSTA, E É PRECISO DIZÊ-LO
//
// Um run cujo `run_id` colida com um stream interno — hoje possível, porque o `POST /runs` não
// valida o `run_id` (eixo do AOS-424) — deixa de ser legível por estas rotas. É a consequência
// PRETENDIDA: um run que partilha stream com o interior do nó já estava a misturar os seus
// eventos com os dele, que é um problema pior do que não o conseguir ler.

import "github.com/aos-ref/substrate/eventstore"

// streamDeRun decide se os eventos lidos pertencem AO RUN cujo id foi pedido.
//
// Devolve false para um stream vazio: a ausência de eventos não é prova de nada, e o chamador
// já tem o seu próprio caminho para esse caso (a posse local, via [apiHandler.runKnown]) — que
// é um facto positivo de outra natureza. Misturar os dois aqui faria esta função responder
// «sim» por falta de evidência, que é exactamente o que ela existe para não fazer.
//
// Exige que TODOS os eventos lidos concordem, e não só o primeiro. Com `Last-Event-ID` a
// leitura começa a meio do stream, pelo que «o primeiro» não é o evento de criação; e num
// stream em que os eventos de um run tenham sido misturados com os de outra coisa, bastaria o
// primeiro concordar para servir o resto. A verificação é sobre eventos que já estão em
// memória e que vão ser serializados a seguir — o custo é irrelevante ao lado disso.
func streamDeRun(runID string, eventos []eventstore.Event) bool {
	if len(eventos) == 0 {
		return false
	}
	for _, ev := range eventos {
		if ev.RunID != runID {
			return false
		}
	}
	return true
}
