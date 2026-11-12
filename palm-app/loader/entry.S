/* The module's entry, first in its code. PceNativeCall jumps here with
 * r0 the emulator state, r1 the 68k side's parameter block and r2 its
 * 68k callback. The block holds, little-endian and read a byte at a time
 * since it may be only 2-byte aligned:
 *
 *   0  the global offset table's address, which every other function
 *      finds its data through (r10)
 *   4  userData, for module_main
 *   8  the top of a stack the 68k side allocated for the module, or 0 to
 *      stay on the system's native stack
 *
 * The entry sets r10, switches stacks if asked, calls
 * module_main(params, call68k), and gives the caller back its r10 and
 * stack on return. */
	.section .text.entry, "ax"
	.global ArmletMain
	.type ArmletMain, %function
ArmletMain:
	stmfd	sp!, {r4, r10, lr}
	ldrb	r10, [r1]
	ldrb	r3, [r1, #1]
	orr	r10, r10, r3, lsl #8
	ldrb	r3, [r1, #2]
	orr	r10, r10, r3, lsl #16
	ldrb	r3, [r1, #3]
	orr	r10, r10, r3, lsl #24
	ldrb	r12, [r1, #8]
	ldrb	r3, [r1, #9]
	orr	r12, r12, r3, lsl #8
	ldrb	r3, [r1, #10]
	orr	r12, r12, r3, lsl #16
	ldrb	r3, [r1, #11]
	orr	r12, r12, r3, lsl #24
	mov	r4, sp
	cmp	r12, #0
	movne	sp, r12
	mov	r0, r1
	mov	r1, r2
	bl	module_main
	mov	sp, r4
	ldmfd	sp!, {r4, r10, lr}
	bx	lr
