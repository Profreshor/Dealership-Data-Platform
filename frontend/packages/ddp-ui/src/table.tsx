import { flexRender, getCoreRowModel, useReactTable, type ColumnDef } from "@tanstack/react-table";
import { useMemo } from "react";

function heading(name: string) {
  return name.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function display(value: unknown) {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export function DataTable({ columns, rows, label }: { columns: string[]; rows: Record<string, unknown>[]; label: string }) {
  const definitions = useMemo<ColumnDef<Record<string, unknown>>[]>(() => columns.map((column) => ({
    id: column,
    accessorFn: (row) => row[column],
    header: heading(column),
    cell: (context) => display(context.getValue()),
  })), [columns]);
  const table = useReactTable({ columns: definitions, data: rows, getCoreRowModel: getCoreRowModel() });

  if (!rows.length) return <div className="empty-state"><strong>No rows yet</strong><span>This table will update when its source data arrives.</span></div>;
  return <div className="table-scroll" role="region" aria-label={`${label} table, horizontally scrollable`} tabIndex={0}>
    <table aria-label={label}>
      <thead>{table.getHeaderGroups().map((group) => <tr key={group.id}>{group.headers.map((header) => <th key={header.id} scope="col">{flexRender(header.column.columnDef.header, header.getContext())}</th>)}</tr>)}</thead>
      <tbody>{table.getRowModel().rows.map((row) => <tr key={row.id}>{row.getVisibleCells().map((cell) => <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>)}</tr>)}</tbody>
    </table>
  </div>;
}
