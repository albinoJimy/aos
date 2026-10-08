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
	// todas são os layouts que o assembler monta, pela ordem; sempre diz quais têm a série
	// publicada mesmo a zero; total conta todos.
	todas  []string
	sempre map[string]bool
	total  map[string]*atomic.Int64
}

// novoRunsPorLayout abre os contadores. A série da 1.5.0 (AOS-514) só é publicada A ZERO num nó
// com a captura do estado opaco ligada: desligada, nenhum run novo fica nesse layout e o
// `/metrics` tem as séries de sempre. Mas um run em 1.5.0 re-hospedado num nó que entretanto a
// desligou — o recuo — NÃO fica invisível: a série aparece assim que conta um (ver
// [runsPorLayout.publicadas]).
func novoRunsPorLayout(capturaDoEstado bool) *runsPorLayout {
	r := &runsPorLayout{todas: agentruntime.SupportedAssemblyVersions(), sempre: map[string]bool{}, total: map[string]*atomic.Int64{}}
	for _, v := range r.todas {
		r.total[v] = new(atomic.Int64)
		r.sempre[v] = v != agentruntime.AssemblyVersion150 || capturaDoEstado
	}
	return r
}

// publicadas devolve os layouts cuja série sai no `/metrics` agora, pela ordem: os de sempre, e
// qualquer outro que já tenha hospedado pelo menos um run.
func (r *runsPorLayout) publicadas() []string {
	if r == nil {
		return nil
	}
	var out []string
	for _, v := range r.todas {
		if r.sempre[v] || r.total[v].Load() > 0 {
			out = append(out, v)
		}
	}
	return out
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
