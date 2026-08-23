package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ENCERRAMENTO GRACIOSO: DOIS DRENOS, DOIS ORÇAMENTOS.
//
// O DEFEITO QUE FECHA, achado H da verificação de funcionamento de 2026-08-23: o MESMO
// `context` de 10 s servia `srv.Shutdown` e `svc.Shutdown`, em série. O
// [net/http.Server.Shutdown] ESPERA pelos pedidos activos e NÃO cancela os contextos dos
// handlers, e o laço SSE de trajectória limpa o write-deadline entre escritas de propósito
// («um wait live ocioso não é limitado»). Logo UM ÚNICO stream aberto e ocioso consumia os
// 10 s inteiros, e o dreno do SERVIÇO recebia um contexto JÁ EXPIRADO: em vez de drenar os
// runs em curso e libertar os leases, cancelava tudo de imediato.
//
// A linha que o nó imprime nesse momento diz «drena runs em curso, liberta leases». Não
// drenava.
//
// # PORQUE NÃO SÃO DOIS ORÇAMENTOS DE 10 s
//
// O `docker-compose.prod.yml` não declara `stop_grace_period`, pelo que vale o default do
// Docker: 10 s entre o SIGTERM e o SIGKILL. O orçamento de 10 s de hoje JÁ consome a folga
// toda. Dar 10+10 não daria mais tempo a ninguém — traria o SIGKILL a meio do dreno do
// serviço, que é pior do que o cancelamento ordenado que temos.
//
// Por isso reparte-se um orçamento TOTAL, e é o total que tem de caber na folga do Docker.
// A fatia maior vai para o serviço: é ele que liberta leases e sela desfechos terminais — o
// trabalho DURÁVEL. O dreno HTTP só deixa acabar pedidos em voo.
//
// # E QUANDO O DRENO HTTP ESGOTA A SUA FATIA
//
// `Shutdown` devolve o erro do contexto e as ligações remanescentes FICAM. Chama-se então
// `Close`, que é o padrão documentado da stdlib: sem isso a fatia do serviço seria gasta a
// competir com um SSE que nunca fecha sozinho.

// ErrBadShutdownBudget — AOS_SHUTDOWN_BUDGET definida e não é uma duração > 0.
var ErrBadShutdownBudget = errors.New("AOS_SHUTDOWN_BUDGET invalido")

const (
	// DefaultShutdownBudget é o orçamento TOTAL do encerramento gracioso.
	//
	// 8 s E NÃO 10 s, E A DIFERENÇA É O PONTO. O default do Docker entre SIGTERM e SIGKILL é
	// 10 s. Um orçamento IGUAL à folga gasta-a inteira e o processo é morto exactamente na
	// fronteira — qualquer overhead (tratar o sinal, esvaziar o log, o próprio `Close`)
	// cai já do lado do SIGKILL, e o último dreno a correr, o do serviço, é o que perde.
	// Ficam 2 s de margem.
	//
	// SUBIR DAQUI EXIGE subir o `stop_grace_period` do compose ao mesmo tempo. É o que o
	// `docker-compose.prod.yml` faz: declara a folga e a env em conjunto, para que a relação
	// entre as duas fique num sítio só e não dependa de ninguém se lembrar.
	DefaultShutdownBudget = 8 * time.Second

	// dockerDefaultGrace é a folga por omissão do Docker entre SIGTERM e SIGKILL. Não é
	// usada em runtime — existe para o teste poder amarrar [DefaultShutdownBudget] à razão
	// de ser dele, em vez de o número ficar a flutuar sem ninguém saber de onde veio.
	dockerDefaultGrace = 10 * time.Second

	// A fatia do dreno HTTP: 2/5 do total. O resto — a MAIORIA — é do serviço, porque é o
	// serviço que faz o trabalho durável (leases, selos terminais).
	fatiaHTTPNum = 2
	fatiaHTTPDen = 5

	// PISO por dreno. Um orçamento de zero não é um dreno curto: é um cancelamento
	// imediato, que é exactamente o defeito que este ficheiro fecha. Nenhuma repartição
	// pode produzi-lo, por mais pequeno que seja o total.
	pisoDeDreno = 100 * time.Millisecond
)

// orcamentoDeEncerramento é a repartição do total pelos dois drenos.
type orcamentoDeEncerramento struct {
	HTTP    time.Duration
	Servico time.Duration
}

// repartirEncerramento reparte o total pelos dois drenos. Função PURA: os testes cobrem cada
// repartição sem levantar um nó.
func repartirEncerramento(total time.Duration) orcamentoDeEncerramento {
	if total <= 0 {
		total = DefaultShutdownBudget
	}
	http := total * fatiaHTTPNum / fatiaHTTPDen
	if http < pisoDeDreno {
		http = pisoDeDreno
	}
	servico := total - http
	if servico < pisoDeDreno {
		servico = pisoDeDreno
	}
	return orcamentoDeEncerramento{HTTP: http, Servico: servico}
}

// encerramentoDoAmbiente lê AOS_SHUTDOWN_BUDGET. Vazio ⇒ [DefaultShutdownBudget].
// Fail-closed: um valor ilegível ABORTA em vez de servir um default que o operador não pediu.
func encerramentoDoAmbiente() (orcamentoDeEncerramento, error) {
	v := strings.TrimSpace(os.Getenv("AOS_SHUTDOWN_BUDGET"))
	if v == "" {
		return repartirEncerramento(DefaultShutdownBudget), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return orcamentoDeEncerramento{}, fmt.Errorf("%w: AOS_SHUTDOWN_BUDGET=%q", ErrBadShutdownBudget, v)
	}
	return repartirEncerramento(d), nil
}

// drenavelHTTP é o ingresso: drena e, se o dreno não couber no orçamento, FECHA à força.
type drenavelHTTP interface {
	Shutdown(context.Context) error
	Close() error
}

// drenavelServico é o loop de serviço: drena runs em curso e liberta leases.
type drenavelServico interface {
	Shutdown(context.Context) error
}

// encerrarGraciosamente corre os dois drenos com orçamentos SEPARADOS.
//
// É uma função à parte, e com interfaces, pela razão que já custou catorze achados a este
// repositório: a unidade pode estar certa e nada a chamar. Assim a cablagem — «o dreno do
// serviço recebe mesmo um contexto vivo quando o dreno HTTP esgotou o dele» — é asseverável
// sem levantar um nó nem abrir um socket.
func encerrarGraciosamente(w io.Writer, srv drenavelHTTP, svc drenavelServico, orc orcamentoDeEncerramento) {
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), orc.HTTP)
	defer cancelHTTP()
	if err := srv.Shutdown(httpCtx); err != nil {
		// As ligações remanescentes NÃO fecham sozinhas — um SSE ocioso limpa o
		// write-deadline entre escritas de propósito. Sem este `Close`, a fatia do serviço
		// seria gasta a competir com elas.
		fmt.Fprintf(w, "[aos] dreno HTTP excedeu %s (%v) — a forcar o fecho das ligacoes remanescentes; a fatia do servico fica INTACTA\n", orc.HTTP, err)
		_ = srv.Close()
	}

	// CONTEXTO NOVO, e é isto o achado: partilhar o de cima devolvia ao serviço um prazo já
	// gasto pelo dreno HTTP.
	svcCtx, cancelSvc := context.WithTimeout(context.Background(), orc.Servico)
	defer cancelSvc()
	_ = svc.Shutdown(svcCtx)
}
