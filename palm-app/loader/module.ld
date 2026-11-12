/* A loadable native ARM module (see README.md): code in one block, data
 * in another, each placed at an address given on the command line
 * (-Ttext and -Tdata; binutils 2.14 will not set the location counter from
 * a --defsym symbol), so cmd/palmlink can link the module at several
 * addresses and find what the loader must relocate.
 *
 * Code holds only position-independent code. Everything code reads or
 * writes as data, read-only data included, sits in the data block, which
 * r10 points at. The global offset table comes first: binutils 2.14
 * measures GOT and GOTOFF references from the start of the output section
 * that holds the table, not from _GLOBAL_OFFSET_TABLE_, so the table must
 * start the block for both to mean the same address. palmlink checks. */
ENTRY(ArmletMain)
SECTIONS
{
	.text : {
		*(.text.entry)
		*(.text)
		*(SORT(.text.*))
		*(.glue_7) *(.glue_7t)
	}
	.data : {
		*(.got.plt) *(.got)
		*(.rodata .rodata.* .rodata1)
		*(.data .data.* .sdata)
	}
	.bss : {
		*(.bss .bss.* .sbss COMMON)
	}
	/DISCARD/ : { *(.comment) *(.note*) }
}
