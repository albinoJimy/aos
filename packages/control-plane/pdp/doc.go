// Package pdp implementa o Policy Decision Point (PDP) do AOS (AOS-004): o par
// de decisão do Reference Monitor (PEP/RM, AOS-003) no contrato C1 RM↔PDP
// (tecnica/12 §4).
//
// O PDP avalia policy-as-code declarativa — a política de referência
// default-deny de tecnica/12 §9 — compilada EM MEMÓRIA no arranque, e devolve
// por cada tool call uma decisão determinística e pura: permit / deny /
// escalate, com um `reason` legível, a versão de política em vigor
// (policy_version) e as obrigações (obligations) que o PEP deve impor.
//
// Motor. A política exprime-se em Cedar (github.com/cedar-policy/cedar-go), que
// é semanticamente equivalente ao Rego da §9 (ver README) e sancionado por
// tecnica/12 §9 ("a escolha é um detalhe de implantação"). Cedar traz uma
// árvore de dependências mínima, alinhada com o pino + hash + assinatura de
// supply-chain do AOS (ADR-005/ADR-012), ao contrário de OPA/Rego.
//
// Bundle assinado e versionado. A política vive num bundle (ficheiros .cedar +
// manifest.json com policy_version SemVer e content_hash sha256 canónico),
// assinado com ed25519 sobre uma mensagem canónica que liga versão e hash. No
// carregamento a assinatura é verificada contra o trust anchor committado
// (trust_anchor.pub); um bundle não-assinado, adulterado ou ausente é rejeitado
// FAIL-CLOSED (ver [ErrSignatureInvalid], [ErrPolicyUnavailable]). O hot-reload
// só aceita uma versão nova e assinada; nunca muta decisões já emitidas.
//
// Integração. O adaptador [PolicyCheck] implementa o hook de política do RM,
// traduzindo o Call em [Input] e a [Decision] em veredicto do RM (incl.
// policy_version, que o RM propaga ao evento de mediação no Event Store).
//
// Âmbito (AOS-004). PDP + bundle assinado/versionado + integração via
// PolicyCheck. Fora de âmbito: identidade real (AOS-005), orçamento/egress
// reais (AOS-007/008) e CI (AOS-010).
package pdp
