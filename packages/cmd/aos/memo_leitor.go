package main

import (
	"context"
	"net/http"
)

// ---------------------------------------------------------------------------------------------
// A CREDENCIAL DO LEITOR VERIFICA-SE UMA VEZ POR PEDIDO — E ISSO PASSA A SER ESTRUTURAL.
//
// O DEFEITO QUE ISTO FECHA, e já mordeu. `POST /autonomy/simular` aplicava a regra de residência
// por cada run distinto, e cada aplicação re-verificava a credencial. O verificador OIDC impõe
// anti-replay por `jti`: a SEGUNDA verificação do mesmo token devolve replay. A rota devolvia
// `avaliados: 0` em produção — com a credencial certa, sem erro e sem log.
//
// Corrigi a rota (PR #96) e depois varri as outras: nenhuma repetia o padrão. Mas um resultado
// negativo obtido por leitura é frágil — volta a partir-se no dia em que alguém escrever um ciclo
// à volta de uma autorização, que é uma coisa perfeitamente razoável de escrever e que não tem
// nada de suspeito à vista. A convenção «chama isto só uma vez» não se defende sozinha.
//
// O QUE MUDA. `barreirasDe` — o invólucro que TODAS as rotas atravessam, porque o registo é
// obrigatório e o valor-zero aborta o arranque — passa a pôr um memo VAZIO no contexto de cada
// pedido. `readGovernance.authorize` consulta-o: se já verificou NESTE pedido, devolve o mesmo
// resultado em vez de ir outra vez ao verificador.
//
// PORQUE ISTO NÃO ENFRAQUECE O ANTI-REPLAY, que é a pergunta certa a fazer. O memo é criado por
// PEDIDO, dentro do invólucro, e vive no contexto DAQUELE pedido. Dois pedidos diferentes com o
// mesmo token têm memos diferentes, e o segundo vai ao verificador e é recusado como replay —
// que é exactamente a propriedade que se quer manter. O que se elimina é a segunda verificação
// do MESMO token dentro do MESMO pedido, que o cliente apresentou uma vez só e que nunca teve
// significado nenhum de segurança: era trabalho nosso a produzir um falso replay.
//
// Um memo indexado pelo TOKEN, em vez de pelo pedido, desligaria o anti-replay por completo e
// pareceria igual em todos os testes de caminho feliz. Por isso o controlo que exige o replay
// ENTRE pedidos distintos é obrigatório aqui, e não opcional.
// ---------------------------------------------------------------------------------------------

// chaveMemoLeitor é o tipo PRÓPRIO da chave de contexto. Um tipo não exportado impede colisão com
// qualquer outro pacote que use a mesma string — a razão pela qual `context` avisa contra chaves
// de tipos básicos.
type chaveMemoLeitor struct{}

// memoLeitor guarda o resultado da ÚNICA verificação de credencial deste pedido.
//
// Não leva mutex: um pedido é servido por uma goroutine, e o memo nunca sai do contexto dele. Se
// algum dia um handler passar o mesmo `*http.Request` a goroutines concorrentes, isto passa a
// precisar de lock — e fica dito aqui em vez de descoberto por um `-race` de madrugada.
type memoLeitor struct {
	feito bool
	id    readerIdentity
	ok    bool
	// causa é a CAUSA da recusa, memorizada a par do `ok`. Sem ela, a segunda consulta no mesmo
	// pedido saberia que foi recusada mas não porquê — e quem escolhe o status do wire
	// ([apiHandler.admitSovereignRead]) cairia no `404` uniforme mesmo quando a causa era uma
	// credencial recusada.
	causa recusaDeLeitura
	// repetidas conta as chamadas a AUTORIZAR que o memo serviu de cache — ou seja, quantas
	// vezes este pedido PEDIU para verificar outra vez. Em produção é inofensivo; num teste é a
	// prova que impede o memo de tornar vacuosa a bateria que guarda o defeito do PR #96.
	repetidas int
}

// comMemoLeitor devolve um pedido cujo contexto carrega um memo VAZIO e NOVO.
//
// Novo por pedido: é essa a fronteira que preserva o anti-replay entre pedidos.
func comMemoLeitor(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), chaveMemoLeitor{}, &memoLeitor{}))
}

// memoDe devolve o memo deste pedido, ou nil se não houver.
//
// nil é um caso LEGÍTIMO, não um erro: um pedido construído à mão num teste, ou um handler que
// derive um contexto novo, não o traz. Sem memo, `authorize` verifica como sempre verificou —
// degrada para o comportamento anterior, nunca para «aceita sem verificar».
func memoDe(r *http.Request) *memoLeitor {
	m, _ := r.Context().Value(chaveMemoLeitor{}).(*memoLeitor)
	return m
}
