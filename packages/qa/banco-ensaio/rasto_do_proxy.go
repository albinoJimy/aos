package bancoensaio

import (
	"regexp"
	"strings"
)

// O RASTO DO PROXY, SEM MENSAGENS (AOS-522, medido a 2026-10-11).
//
// Quando a sonda recebe um 5xx, a causa está no registo do proxy — e o contentor, criado com
// `--rm`, leva-o consigo. Na qualificação do Kimi de produção a sonda deu 500 três vezes e o
// banco só dizia «outro». Este ficheiro reduz o registo ao que não pode levar conteúdo nem
// segredos: de cada quadro de um rasto de erro fica o ficheiro, a linha e a função; de cada linha
// que abre por um nome de excepção fica SÓ o nome. O texto das mensagens — que pode trazer o
// corpo da resposta do fornecedor, com identificadores da conta — nunca sai.

var (
	quadroDoRasto   = regexp.MustCompile(`File "[^"]*?([A-Za-z0-9_.-]+)", line (\d+), in ([A-Za-z_<>][A-Za-z0-9_<>]*)`)
	excepcaoDoRasto = regexp.MustCompile(`^\s*((?:[A-Za-z_][A-Za-z0-9_]*\.)*[A-Za-z_][A-Za-z0-9_]*(?:Error|Exception|Timeout))\b`)
)

// rastoSemMensagens devolve os últimos `max` elementos do rasto, separados por ` | `.
func rastoSemMensagens(logs string, max int) string {
	var partes []string
	for _, linha := range strings.Split(logs, "\n") {
		if m := quadroDoRasto.FindStringSubmatch(linha); m != nil {
			partes = append(partes, m[1]+":"+m[2]+" "+m[3])
			continue
		}
		if m := excepcaoDoRasto.FindStringSubmatch(linha); m != nil {
			partes = append(partes, "["+m[1]+"]")
		}
	}
	if len(partes) > max {
		partes = partes[len(partes)-max:]
	}
	return strings.Join(partes, " | ")
}
