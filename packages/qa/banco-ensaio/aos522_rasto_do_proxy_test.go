package bancoensaio

import (
	"strings"
	"testing"
)

// O RASTO DO PROXY SEM MENSAGENS (AOS-522). Do registo do proxy só saem o ficheiro, a linha e a
// função de cada quadro, e o NOME de cada excepção. O texto das mensagens — onde o proxy cola o
// corpo da resposta do fornecedor, com identificadores da conta e, num erro de autenticação,
// pedaços da chave — nunca sai.
func TestAOS522_RastoDoProxy_SoNomesNuncaMensagens(t *testing.T) {
	const segredo = "sk-kimi-SENTINELA-DA-CHAVE-0123456789"
	const conta = "org-SENTINELA-DA-CONTA"
	logs := strings.Join([]string{
		`INFO:     172.20.0.3:55080 - "POST /v1/chat/completions HTTP/1.1" 500 Internal Server Error`,
		`Traceback (most recent call last):`,
		`  File "/usr/lib/python3.13/site-packages/litellm/proxy/proxy_server.py", line 3512, in chat_completion`,
		`    result = await base_llm_response_processor.base_process_llm_request(` + segredo + `)`,
		`  File "/usr/lib/python3.13/site-packages/litellm/router.py", line 987, in acompletion`,
		`    raise e`,
		`litellm.exceptions.InternalServerError: litellm.InternalServerError: OpenAIException - {"error":{"message":"conta ` + conta + ` chave ` + segredo + `"}}`,
		`  KeyError: 'usage ` + conta + `'`,
		`uma linha qualquer com ` + segredo,
	}, "\n")
	r := rastoSemMensagens(logs, 14)
	quer := `proxy_server.py:3512 chat_completion | router.py:987 acompletion | [litellm.exceptions.InternalServerError] | [KeyError]`
	if r != quer {
		t.Errorf("rasto =\n  %s\nquer\n  %s", r, quer)
	}
	for _, proibido := range []string{segredo, conta, "sk-kimi", "org-", "message", "{", "/usr/lib"} {
		if strings.Contains(r, proibido) {
			t.Errorf("o rasto leva %q: %s", proibido, r)
		}
	}
	// Só os últimos `max` elementos, e nada quando o registo não tem rasto.
	if r := rastoSemMensagens(logs, 2); r != `[litellm.exceptions.InternalServerError] | [KeyError]` {
		t.Errorf("tecto de elementos: %s", r)
	}
	if r := rastoSemMensagens("INFO: tudo bem\nmais uma linha com "+segredo, 14); r != "" {
		t.Errorf("registo sem rasto devolveu %q", r)
	}
}
