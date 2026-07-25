// Package attestation VERIFICA attestations de dispositivo WebAuthn (formatos "packed" e
// "fido-u2f") contra uma ALLOWLIST DE AAGUID e âncoras de confiança x509 — a peça que faz o
// 4-eyes do AOS deixar de ser só estrutural (AOS-177, EPIC-16 frente 4, ADR-016 §4).
//
// # Porque é que isto é um MÓDULO À PARTE
//
// O attestationObject WebAuthn é CBOR, e a chave pública da credencial é COSE (CBOR). Não há
// descodificador CBOR na stdlib do Go. A Carta do AOS (emenda 1.3) abre uma EXCEÇÃO ESCOPADA
// ao ADR-017 (binário do nó zero-dep): a lib CBOR é permitida APENAS no componente de
// autoridade de identidade EXTERNO — nunca no grafo de build do nó.
//
// Este módulo é essa exceção, e é a sua ÚNICA morada:
//
//   - go.mod PRÓPRIO, com uma única dependência externa: github.com/fxamacker/cbor/v2
//     (descodificador CBOR/COSE vetado; a sua única dep transitiva é github.com/x448/float16).
//     Nenhum framework WebAuthn é usado — toda a criptografia é stdlib (crypto/x509,
//     crypto/ecdsa, crypto/rsa, crypto/ed25519, crypto/sha256, crypto/subtle).
//   - o módulo NÃO importa nenhum módulo do AOS, e nenhum módulo do AOS o importa. É
//     consumido através de uma PORTA STDLIB definida em packages/integration
//     ([integration.DeviceAttestationVerifier]) e satisfeita ESTRUTURALMENTE por
//     [Verifier.VerifyDeviceAttestation] — pelo que nem sequer há uma aresta de importação
//     por onde a dependência pudesse escorregar para o nó.
//   - a invariante é EXECUTÁVEL, não prosa: packages/cmd/aos/dep_isolation_test.go corre
//     `go list -deps ./...` sobre o módulo do nó e FALHA se cbor/float16/webauthn ou este
//     módulo aparecerem no fecho transitivo; packages/integration tem o guarda equivalente.
//     E TestPortConformance_MatchesIntegrationPort (neste módulo) parseia o ficheiro da
//     porta e falha se a assinatura divergir — a conformidade estrutural não é confiada à
//     memória de ninguém.
//
// # Postura
//
// FAIL-CLOSED em tudo. Não há caminho que devolva "aceite" por omissão: allowlist de AAGUID
// default-deny, "none" sempre recusado, x5c sem âncoras de confiança recusado,
// self-attestation recusada salvo opt-in explícito, algoritmos fora da lista curta
// (ES256/RS256/EdDSA) recusados, CBOR com limites anti-DoS (profundidade/cardinalidade/
// tamanho, indefinite-length e tags proibidos) e todos os índices do parse binário de
// authData validados antes do uso (nenhum panic por entrada hostil).
//
// Os erros são sentinelas comparáveis com errors.Is e NÃO transportam segredos, PII, nem
// bytes da credencial — no máximo o AAGUID (identificador de MODELO do autenticador) em hex.
package attestation
