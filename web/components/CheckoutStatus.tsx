"use client";

import { CameraIcon, HouseIcon } from "@phosphor-icons/react";
import { useEffect, useState } from "react";
import {
  ReceiptPrinter,
  type ReceiptPrinterStage,
} from "@/components/ReceiptPrinter";
import { TactileButton } from "@/components/TactileButton";

export interface ShoppingReceiptItem {
  groupKey: string;
  ean?: string;
  title: string;
  quantity: number;
  unitPrice: number;
  subtotal: number;
  checked: boolean;
  available?: boolean;
}

export interface CheckoutStatusProps {
  listId: string;
  storeName: string;
  items: ShoppingReceiptItem[];
  onOpenCameraCompare?: () => void;
}

function Logo() {
  return (
    <img
      alt=""
      className="size-6"
      src="/images/receipt-printer-logo.png"
    />
  );
}

const fmtARS = new Intl.NumberFormat("es-AR", {
  style: "currency",
  currency: "ARS",
  maximumFractionDigits: 0,
});

export function CheckoutStatus({
  listId,
  storeName,
  items,
  onOpenCameraCompare,
}: CheckoutStatusProps) {
  const [stage, setStage] = useState<ReceiptPrinterStage>("processing");

  useEffect(() => {
    const tPrint = setTimeout(() => setStage("printing"), 650);
    const tDone = setTimeout(() => setStage("complete"), 2450);
    return () => {
      clearTimeout(tPrint);
      clearTimeout(tDone);
    };
  }, []);

  const checkedItems = items.filter((i) => i.checked);
  const pendingItems = items.filter((i) => !i.checked);

  const totalToPay = checkedItems.reduce((acc, i) => acc + (i.subtotal || 0), 0);
  const totalPending = pendingItems.reduce((acc, i) => acc + (i.subtotal || 0), 0);

  return (
    <div className="flex flex-col items-center gap-4">
      <ReceiptPrinter.Root stage={stage}>
        <ReceiptPrinter.Machine>
          <ReceiptPrinter.Header>
            <Logo />
            <TactileButton depth="shallow" href="/" size="sm">
              <HouseIcon aria-hidden="true" size={13} weight="fill" />
              Home
            </TactileButton>
          </ReceiptPrinter.Header>

          <ReceiptPrinter.Screen>
            <div className="space-y-4">
              <div className="flex justify-between">
                <div>
                  <p className="font-semibold">{storeName}</p>
                  <p className="text-xs opacity-75">
                    {checkedItems.length} en chango · {pendingItems.length} pendientes
                  </p>
                </div>
                <div className="text-right">
                  <span className="block text-[11px] opacity-70">Total</span>
                  <strong className="text-xl">{fmtARS.format(totalToPay)}</strong>
                </div>
              </div>
              <ReceiptPrinter.Status />
            </div>
          </ReceiptPrinter.Screen>
        </ReceiptPrinter.Machine>

        <ReceiptPrinter.Output>
          <ReceiptPrinter.Paper>
            <h2>Ticket de Compra — {storeName}</h2>
            <hr />

            {/* Productos a pagar (en el chango) */}
            <dl className="space-y-1.5 text-xs">
              {checkedItems.map((it) => (
                <div key={it.groupKey} className="flex justify-between">
                  <dt>
                    {it.quantity}x {it.title}
                  </dt>
                  <dd>{fmtARS.format(it.subtotal)}</dd>
                </div>
              ))}
              <div className="flex justify-between pt-2 font-bold text-sm">
                <dt>TOTAL A PAGAR</dt>
                <dd>{fmtARS.format(totalToPay)}</dd>
              </div>
            </dl>

            {/* Productos pendientes */}
            {pendingItems.length > 0 && (
              <>
                <hr className="my-3" />
                <p className="text-xs font-bold uppercase">
                  Productos Pendientes ({pendingItems.length})
                </p>
                <dl className="space-y-1 text-xs opacity-75">
                  {pendingItems.map((it) => (
                    <div key={it.groupKey} className="flex justify-between">
                      <dt>
                        {it.quantity}x {it.title}
                      </dt>
                      <dd>{fmtARS.format(it.subtotal)}</dd>
                    </div>
                  ))}
                  <div className="flex justify-between pt-1 font-semibold">
                    <dt>Subtotal pendiente</dt>
                    <dd>{fmtARS.format(totalPending)}</dd>
                  </div>
                </dl>
              </>
            )}

            <hr className="my-3" />
            <p className="text-xs"> Lista: {listId} · Gracias por usar Lita.</p>
          </ReceiptPrinter.Paper>
        </ReceiptPrinter.Output>
      </ReceiptPrinter.Root>

      {stage === "complete" && (
        <button
          type="button"
          onClick={onOpenCameraCompare}
          className="inline-flex items-center gap-2 rounded-xl bg-emerald-500 px-5 py-3 font-bold text-sm text-zinc-950 shadow-lg transition hover:bg-emerald-400"
        >
          <CameraIcon size={18} weight="bold" />
          Comparar ticket
        </button>
      )}
    </div>
  );
}
