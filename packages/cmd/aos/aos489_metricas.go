package main

// AOS-489 — O QUE SE VÊ SEM LER O WAL: o layout do prompt em uso e a repetição de tool calls.
//
// O ticket nasceu de uma medição feita à mão sobre o `events.wal` de produção: 45 das 75 tool
// calls eram o modelo a repetir uma chamada. Nada no nó o dizia. E o layout em que cada run é
// montado — que desde este ticket pode ser o antigo, num run retomado — também só se via no log.
// Estas duas famílias do `/metrics` são essa leitura.

import (
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// medicaoDeToolCalls soma, por processo, as tool calls despachadas e as REPETIDAS — a mesma tool
// com os mesmos argumentos (os que o modelo emitiu) já pedida antes no mesmo run, permitida ou
// negada. É o [agentruntime.ToolCallStats] do runtime do nó.
//
// PORQUE A CONTAGEM VIVE NO KERNEL E SÓ A SOMA AQUI. Saber se uma chamada repete outra exige a
// história das tool calls do run, e quem a tem é a sequência de segmentos do loop — que precisa
// dela para o aviso de repetição, e que o motor de replay refaz. Contar no nó obrigaria a uma
// segunda história por run, com outra definição de «igual» à espera de divergir da do aviso. O nó
// recebe números prontos por uma porta do kernel (o sentido de dependências de sempre) e limita-se
// a expô-los.
//
// O QUE A SOMA NÃO É. É por processo e desde o arranque: não distingue runs, e um run
// RE-HOSPEDADO (retoma, crash-resume) volta a percorrer os turnos já dados e soma-os outra vez.
// Serve para a TAXA (repetidas / despachadas) e a sua tendência, não para contar chamadas de um
// run — isso continua no log.
type medicaoDeToolCalls struct {
	despachadas atomic.Int64
	repetidas   atomic.Int64
}

// observar é o [agentruntime.ToolCallStats]. Nil-safe: um nó montado à mão num teste, sem a
// medição, não rebenta.
func (m *medicaoDeToolCalls) observar(_ string, despachadas, repetidas int) {
	if m == nil {
		return
	}
	m.despachadas.Add(int64(despachadas))
	m.repetidas.Add(int64(repetidas))
}

// runsPorLayout conta os runs HOSPEDADOS por este processo, por layout de montagem do prompt. O
// vocabulário é FECHADO — os layouts que o assembler sabe montar
// ([agentruntime.SupportedAssemblyVersions]) —, pelo que o rótulo `assembly_version` nunca leva
// texto de terceiros: um run que chegasse com outra versão não é contado (e não corre: o runtime
// recusa-o).
type runsPorLayout struct {
	versoes []string
	total   map[string]*atomic.Int64
}

func novoRunsPorLayout() *runsPorLayout {
	r := &runsPorLayout{versoes: agentruntime.SupportedAssemblyVersions(), total: map[string]*atomic.Int64{}}
	for _, v := range r.versoes {
		r.total[v] = new(atomic.Int64)
	}
	return r
}

// contar soma um run hospedado no layout dado; devolve false se o layout não é um dos suportados.
func (r *runsPorLayout) contar(versao string) bool {
	if r == nil {
		return false
	}
	c, ok := r.total[versao]
	if ok {
		c.Add(1)
	}
	return ok
}

// lido devolve o total de um layout suportado.
func (r *runsPorLayout) lido(versao string) int64 {
	if r == nil || r.total[versao] == nil {
		return 0
	}
	return r.total[versao].Load()
}
