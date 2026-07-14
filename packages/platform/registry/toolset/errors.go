package toolset

import "errors"

// Erros sentinela do congelamento de tool set por run (AOS-050). Todos FAIL-CLOSED:
// um congelamento que não possa produzir uma superfície de tools íntegra e
// inequívoca é RECUSADO, nunca degradado silenciosamente.
var (
	// ErrNoCatalog — o catálogo (REG) fornecido é nil. Sem fonte de tools não há
	// tool set a congelar.
	ErrNoCatalog = errors.New("toolset: catálogo (REG) nil")

	// ErrNoRunID — o run_id do congelamento está vazio. O snapshot congelado está
	// SEMPRE ligado à identidade do run (é imutável durante a vida DESSE run); sem
	// run_id não há a que o ligar.
	ErrNoRunID = errors.New("toolset: run_id vazio")

	// ErrAmbiguousToolID — o conjunto active contém MAIS DO QUE UMA versão da mesma
	// tool (mesmo id). A superfície de um run tem de apresentar cada tool UMA vez,
	// por uma versão pinada inequívoca — duas versões active do mesmo id tornariam
	// ambígua tanto a superfície do prompt como a expectativa de digest por id que
	// a revalidação por chamada (AOS-051) consulta. Fail-closed: o operador tem de
	// deprecar a versão anterior antes de activar a nova.
	ErrAmbiguousToolID = errors.New("toolset: id de tool com múltiplas versões active (superfície ambígua)")

	// ErrControlByte — um campo projectado para o snapshot (id, versão, digest ou
	// origem de servidor MCP) contém um byte de CONTROLO (C0 < 0x20 ou DEL 0x7f).
	// Esse conjunto inclui os DELIMITADORES de que os serializers a jusante
	// dependem para separar campos — \0 e \x1e no hash do conjunto (computeHash) e
	// \t e \n no prefixo imutável do prompt (buildPrefix). Admitir um desses bytes
	// tornaria as fronteiras ambíguas: duas identidades pinadas DISTINTAS poderiam
	// colidir no mesmo toolset_hash (perda de injectividade da testemunha de
	// imutabilidade) ou fundir/injectar linhas no prefixo byte-idêntico. A
	// injectividade do hash e a estabilidade byte-a-byte do prefixo assentavam
	// numa invariante de charset NÃO imposta; este erro impõe-na no congelamento,
	// fail-closed, sem a assumir do gate de admissão do REG.
	ErrControlByte = errors.New("toolset: campo com byte de controlo (fronteira de serialização ambígua)")
)
