// Package taint é o primitivo canónico de taint tracking do AOS (ADR-005): o
// rótulo de confiança (control/data-plane) e a sua propagação por derivações. É
// o vocabulário partilhado que o Agent Runtime (marcação na origem + separação
// dual-LLM/CaMeL) e o Reference Monitor (enforcement no gate) usam sem ciclo — é
// um pacote ZERO-DEP e FOLHA no kernel: não importa nada além da stdlib, pelo que
// tanto o RT como o RM o podem importar (o RT já importa o RM; nenhum importa este
// pacote de volta). Não conhece o loop, o RM nem a memória — só o reticulado de
// confiança.
//
// # Modelo de confiança (label.go)
//
// Um dado tem um [Label]: [Trusted] (system + utilizador autenticado) ou
// [Untrusted] (tudo o resto — tool results, web, memória não-confiável, schemas
// MCP, saída do modelo). O rótulo é IMPOSTO PELO TIPO e FAIL-CLOSED: o valor-zero
// de [Label] é [Untrusted], logo um dado que nunca foi explicitamente marcado
// trusted é tratado como untrusted. A origem determina o rótulo por [LabelFor];
// origens desconhecidas classificam untrusted.
//
// # Propagação (propagation.go)
//
// O taint propaga-se por DERIVAÇÕES segundo o join (least-upper-bound) do
// reticulado {trusted ⊑ untrusted}: trusted ⊔ untrusted = untrusted (ver [Join]).
// Um dado derivado de (pelo menos um) input untrusted é untrusted — não há
// caminho na API que "lave" o untrusted para trusted (não existe desclassificação:
// a promoção é estruturalmente impossível). [Value] carrega o payload, o rótulo e
// a PROVENIÊNCIA (as origens que contribuíram); [Derive] compõe pais mantendo o
// join dos rótulos e a UNIÃO das proveniências — o forense sobrevive à derivação
// (memory poisoning, ASI06).
//
// # Composição com a memória
//
// [Origin] espelha byte-a-byte o conjunto de fontes de proveniência da memória
// (platform/memory/domain.ProvenanceSource), pelo que a proveniência de um registo
// de memória compõe-se com este reticulado por LabelFor(Origin(mem.Source)) SEM
// que este pacote importe a memória (respeita a layering: o kernel não depende da
// plataforma).
package taint
