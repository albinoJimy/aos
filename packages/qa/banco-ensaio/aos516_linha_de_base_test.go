package bancoensaio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// AOS-516 — SEM PERFIL QUE DEVOLVA ESTADO, O BANCO É O DE ANTES.
//
// Os dois digests abaixo foram calculados no commit 2823f5ac, ANTES de o banco saber ligar a
// captura do estado, a governação da rota e o layout 1.5.0. Uma corrida da bateria sem perfil
// tem de continuar a enviar exactamente os mesmos pedidos e a escrever exactamente o mesmo
// relatório: se um destes digests mudar, o caminho de sempre deixou de ser o de sempre.
//
// AOS-525 mudou UMA coisa no relatório, de propósito: o texto da definição da taxa de factos
// ausentes, que passou a dizer a regra nova (sem acentos, sem maiúsculas, sem espaços
// repetidos). O digest de antes continua a ser a âncora: o teste repõe essa frase e compara
// o resto, byte a byte, com o relatório de antes. Se a frase não estiver lá, falha.
const (
	aos516PedidosDeAntes   = "sha256:4dd09e4b398312fdcef23d0ecac1a8576e4c8fa6e9f12f5342452af7144d6cf2"
	aos516RelatorioDeAntes = "sha256:4e769fb22172055c7b4af15de32e1c6ddced2dfd3f4b52393b0d1c86002ff38d"
)

// aos525FraseDaRegraDosFactos é o que o AOS-525 acrescentou à definição da taxa de factos
// ausentes; tirada do relatório, ele tem de ser o de antes.
const aos525FraseDaRegraDosFactos = ", comparados sem acentos, sem maiusculas e sem espacos repetidos"

// aos516Digests corre a bateria inteira no braço A contra o provider falso do roteiro por
// omissão, sem perfil, e devolve o digest dos corpos dos pedidos e o do relatório em JSON.
func aos516Digests(t *testing.T) (pedidos, relatorio string) {
	t.Helper()
	amb := novoAmbienteFalso(t, nil, nil)
	plano, err := planoDasOpcoes(amb.bateria, opcoes{experiencia: string(ExperienciaBateria), braco: string(BracoA), semente: 516})
	if err != nil {
		t.Fatal(err)
	}
	r := amb.correr(t, plano, nil)
	soma := sha256.New()
	for _, corpo := range amb.falso.Corpos() {
		soma.Write(corpo)
		soma.Write([]byte{'\n'})
	}
	cru, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(cru, []byte(aos525FraseDaRegraDosFactos)) {
		t.Fatalf("a definicao da taxa de factos ausentes nao diz a regra do AOS-525")
	}
	cru = bytes.ReplaceAll(cru, []byte(aos525FraseDaRegraDosFactos), nil)
	doRelatorio := sha256.Sum256(cru)
	return "sha256:" + hex.EncodeToString(soma.Sum(nil)), "sha256:" + hex.EncodeToString(doRelatorio[:])
}

func TestAOS516_SemPerfilQueDevolva_PedidosERelatorioSaoOsDeAntes(t *testing.T) {
	pedidos, relatorio := aos516Digests(t)
	if outra, outro := aos516Digests(t); outra != pedidos || outro != relatorio {
		t.Fatalf("a corrida nao e determinista: os digests de duas corridas iguais diferem")
	}
	if pedidos != aos516PedidosDeAntes {
		t.Errorf("os pedidos de uma corrida sem perfil mudaram: %s (antes do AOS-516: %s)", pedidos, aos516PedidosDeAntes)
	}
	if relatorio != aos516RelatorioDeAntes {
		t.Errorf("o relatorio de uma corrida sem perfil mudou: %s (antes do AOS-516: %s)", relatorio, aos516RelatorioDeAntes)
	}
}
