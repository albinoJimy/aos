package main

// AUTORIDADE SOBRE A DESTRUIÇÃO DE DADOS (AOS-367).
//
// O DEFEITO QUE ISTO FECHA. As quatro rotas de `planoGovernacao` — `POST /dsar/erase`,
// `/dsar/hold`, `/dsar/release` e `/dsar/expire` — autorizavam o crypto-shred IRREVERSÍVEL com
// nada além de um ID-token OIDC de LEITURA verificado (`readGov.authorize`). Um só par
// issuer/audience serve o leitor de runs e o operador que destrói: quem tem credencial para LER
// runs da sua região tinha, com a mesma credencial, autoridade para DESTRUIR os dados de um
// titular. O contraste estava no mesmo binário: o `POST /autonomy` — uma acção REVERSÍVEL e
// selada — exige capability própria, assinatura ed25519 sobre payload canónico com nonce durável,
// e uma segunda assinatura para atravessar o limiar do gate humano.
//
// O QUE PASSA A VALER, e porquê nesta forma:
//
//   - Uma capability PRÓPRIA, `dsar:erase`, distinta de autonomy:set/steer/pause. Vive em
//     AOS_DSAR_ERASERS — a lista dos emitterIDs de AOS_OPERATORS que a detêm — e não numa extensão
//     da gramática de AOS_OPERATORS, para não mexer no parser de um registo de chaves por causa de
//     UM conjunto de rotas (a MESMA decisão de [parseAutonomySetters]). Um operador fora da lista
//     assina e é recusado.
//   - RETRO-COMPATÍVEL POR COMPOSIÇÃO (como o TaintGate de AOS-363): lista vazia ⇒ a prova está
//     DESLIGADA e as rotas mantêm a autenticação por leitura que sempre tiveram (dev, testes por
//     headers). Lista não-vazia ⇒ a prova é EXIGIDA. Em produção a guarda de arranque
//     [ErrProductionNeedsDSARErasers] garante que nunca é vazia — a destruição irreversível não
//     pode ficar autorizada por um token de leitura sozinho.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// dsarEraseCapability é a capability que um emissor de AOS_OPERATORS tem de deter para assinar uma
// acção DSAR de destruição. Nomeada no vocabulário `<domínio>:<acção>` das outras capabilities de
// governação (`autonomy:set`, `approve:<classe>`, `ratify:production`).
const dsarEraseCapability = "dsar:erase"

// ErrBadDSARErasers — AOS_DSAR_ERASERS nomeia um operador que NÃO consta de AOS_OPERATORS, ou está
// malformada. Fail-closed no arranque: uma capability atribuída a um emitterID sem pubkey seria um
// operador que "pode" destruir e nunca autentica — o mesmo defeito de [ErrBadAutonomySetters],
// visto do lado da destruição de dados.
var ErrBadDSARErasers = errors.New("aos: AOS_DSAR_ERASERS invalida — lista de emitterIDs (separados por virgula) que constam de AOS_OPERATORS e detem a capability dsar:erase; um id ausente de AOS_OPERATORS, vazio ou duplicado aborta o arranque")

// parseDSARErasers lê a lista `id1,id2` de emitterIDs com `dsar:erase`. Vazio ⇒ (nil, nil): a prova
// de autoridade DSAR fica DESLIGADA (comportamento legado), declarado no README. A verificação de
// que cada id CONSTA de AOS_OPERATORS é do [Bootstrap], que é onde as duas listas se encontram —
// idêntica a [parseAutonomySetters].
func parseDSARErasers(s string) ([]string, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue // vírgula final ou dupla ⇒ ruído tolerável, não um typo.
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w: emitterID %q duplicado", ErrBadDSARErasers, id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: nenhuma entrada valida em %q", ErrBadDSARErasers, s)
	}
	sort.Strings(out) // ordem determinista ⇒ banner e erros reproduzíveis.
	return out, nil
}
