// Package authz é o núcleo de ESCOPO do Reference Monitor (AOS-071): computa e
// impõe a autoridade escopada ao principal — a invariante autoridade =
// utilizador ∩ classe de agente (ADR-003) — ao longo da cadeia de delegação
// on-behalf-of, cortando o padrão confused deputy.
//
// # Invariante central (ADR-003)
//
// A autoridade EFECTIVA de um principal é a INTERSECÇÃO das capabilities do
// utilizador humano responsável com as da(s) classe(s) de agente que agem
// on-behalf-of dele. Uma tool call cuja capability NÃO pertence a essa
// intersecção é NEGADA (default-deny, ADR-011). O resultado é sempre subconjunto
// de AMBOS os eixos (menor privilégio): nem o utilizador nem a classe alargam o
// outro.
//
// # Restrição monotónica na cadeia (só restringe, nunca amplia)
//
// Ao descer a cadeia on-behalf-of (raiz humana → agente → sub-agente), a
// autoridade efectiva só pode INTERSECTAR — nunca unir/alargar. O escopo de um
// sub-agente = intersecção do escopo do delegante com a sua própria classe. A
// prova é ESTRUTURAL: [FoldScope] é uma dobra de intersecções (⊆ monotonicamente
// decrescente); não existe operação nesta API que alargue um escopo. Uma
// tentativa EXPLÍCITA de um elo reclamar autoridade acima do delegante é
// detectada por [CheckNoEscalation] / [RestrictAlong] e devolve
// [ErrScopeEscalation] — negada e atribuível.
//
// # Composição com o taint (AOS-069)
//
// O escopo efectivo é derivado EXCLUSIVAMENTE da identidade (utilizador + classe,
// via [AuthoritySource]) — nunca do CONTEÚDO do pedido. Logo, conteúdo untrusted
// não pode, estruturalmente, elevar a autoridade efectiva: seja qual for o rótulo
// de taint, a intersecção computada é a mesma. O ScopeGate compõe o TaintGate
// (AOS-069) sem o duplicar: o taint corta a autorização untrusted de capabilities
// privilegiadas; o escopo garante o menor privilégio identitário.
//
// # Zero dependências
//
// Só stdlib (sort/errors): é um primitivo do kernel. NÃO importa platform/* (a
// intersecção espelha o padrão de model-gateway/pipeline/authn.EffectiveAuthority
// de AOS-057, reimplementado aqui como primitivo zero-dep para não criar ciclos
// nem acoplar layers). A decisão é DETERMINISTA e PURA (sem relógio/rand): função
// apenas de (autoridade-fonte, cadeia, capability).
package authz
