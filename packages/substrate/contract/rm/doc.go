// Package rm contém o contrato canónico do Reference Monitor (AOS-003, contrato C1).
//
// Vive no substrato para ser importável por todos os pilares (kernel, platform,
// control-plane via re-exportação no kernel) sem violar as fronteiras de camada.
// O kernel/reference-monitor implementa a lógica de mediação e minta o Permit
// opaco; este pacote apenas define a superfície de dados partilhada.
package rm
