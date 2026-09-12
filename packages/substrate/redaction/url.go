package redaction

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Redacção ESTRUTURAL de endereços, distinta do motor de PII deste pacote.
//
// O resto do módulo detecta PII por padrão e aplica política; isto não detecta nada — recompõe
// uma URL a partir dos dois campos que se querem preservar e deita fora todos os outros. Vive
// aqui porque este é o ÚNICO módulo folha que os três sítios que precisam dela podem importar:
// `packages/integration` e `packages/cmd/aos` (composition-roots) e `packages/platform/broker`
// (camada `platform`, para quem `platform → substrate` é canónico — `scripts/ci/layer-lint.sh`).
//
// PORQUE AQUI E NÃO COPIADO EM CADA UM (AOS-337). Houve três cópias durante um commit, e a
// terceira divergiu da primeira no dia em que nasceu: uma devolvia `(inválido)` e a outra
// `(inválida)`, o que basta para um `grep` sobre logs agregados perder metade dos casos. Dois
// redactores que discordam são piores do que um — e a fronteira de camadas nunca obrigou à
// cópia: obrigava só a que o código partilhado descesse a `substrate`, que é o que este ficheiro
// faz.

// URL devolve a forma PUBLICÁVEL de um endereço: esquema, host e porta, e mais nada.
//
// Descarta user-info, caminho, query e fragmento — tudo o que possa carregar segredo. Um
// endereço que o parser recuse devolve `(inválida)` e NUNCA o valor original: uma URL que não se
// sabe analisar não se sabe redigir, e é precisamente numa URL malformada que uma credencial mal
// escapada tem mais probabilidade de estar.
//
// É deliberadamente mais estreita do que o necessário. Preservar o caminho ajudaria o
// diagnóstico, mas um caminho já carrega segredo por si em sistemas reais — o path de um segredo
// no KV v2 diz QUAL a credencial em causa, mesmo sem revelar o seu valor.
func URL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(inválida)"
	}
	// Composto a partir dos DOIS campos preservados, em vez de concatenado à mão: um endereço
	// sem esquema continua re-analisável como a mesma coisa que entrou.
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// TransportError devolve um erro de transporte com todos os endereços redigidos.
//
// O `net/http` já redige a SENHA nos `*url.Error` que constrói (`http://admin:***@host/…`) mas
// deixa o UTILIZADOR intacto — e num endereço de Vault é ele que identifica o principal.
//
// Preserva o `Op` e a CAUSA, que é o que diagnostica: DNS, recusa de ligação, TLS. Um erro que
// não seja `*url.Error` passa tal-qual — não se inventa redacção sobre uma forma que não se
// conhece.
//
// DESCE PELOS `*url.Error` ANINHADOS. Um `url.Error` cujo `Err` seja outro `url.Error` — o que
// acontece quando o transporte injectado falha contra um proxy — reimprimiria o interior INTEIRO
// se só se olhasse para o de fora, e é aí que uma credencial de terceiros sairia por extenso.
//
// SEMÂNTICA DO `errors.As`: encontra o `*url.Error` MAIS EXTERIOR da cadeia, pelo que texto de
// um wrapper acima dele não sobrevive. É deliberado — a alternativa (asserção de tipo só no topo)
// deixaria passar CRU um `url.Error` embrulhado, que é a fuga que esta função existe para fechar.
//
// LIMITE DECLARADO: a causa pode conter uma URL em TEXTO, não estruturada — o `net/http` produz
// `failed to parse Location header "…"` a partir de um cabeçalho que o SERVIDOR controla. Isso
// não é redigível estruturalmente e não é omitido, porque omitir a causa custaria o diagnóstico
// que esta função existe para preservar. É conteúdo do interlocutor, não segredo do nó.
func TransportError(addr string, err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	// CADA NÍVEL É REDIGIDO COM O SEU PRÓPRIO ENDEREÇO. Usar o do chamador em todos os
	// níveis apagaria o endereço interior — seguro, mas perderia a informação de que a
	// falha foi contra o proxy e não contra o Vault. `addr` é só o recurso para quando o
	// erro não traz endereço nenhum.
	alvo := ue.URL
	if strings.TrimSpace(alvo) == "" {
		alvo = addr
	}
	return fmt.Errorf("%s %s: %w", ue.Op, URL(alvo), TransportError("", ue.Err))
}
