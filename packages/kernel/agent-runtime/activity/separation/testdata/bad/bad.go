// Package bad é um CONSUMIDOR-EXEMPLO incorrecto para o lint de separação: escreve
// efeitos externos (rede, ficheiro, processo) DIRECTAMENTE na lógica do loop, fora
// de qualquer activity — exactamente o que o contrato de AOS-021 proíbe. NÃO é
// compilado pelo módulo (vive em testdata); serve só de entrada ao analisador.
package bad

import (
	"net/http"
	"os"
	"os/exec"
)

// runTurn contém TRÊS efeitos externos directos (não-mediados, não-registados,
// irreproduzíveis em replay): uma chamada de rede, um I/O de ficheiro e um processo.
func runTurn(url, path string) ([]byte, error) {
	// VIOLAÇÃO 1: rede directa no loop.
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	_ = resp

	// VIOLAÇÃO 2: I/O de ficheiro directo no loop.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_ = f

	// VIOLAÇÃO 3: execução de processo directa no loop.
	cmd := exec.Command("echo", "efeito")
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return nil, nil
}
