import React, { useId, useRef } from 'react'
import { UploadCloud, X } from 'lucide-react'
import { cn } from '@/lib/util'
import { randomUUID } from '../../lib/util'
export type UploadFile = {
  uid: string
  name: string
  originFileObj?: File
  size?: number
}
type Props = {
  className?: string
  accept?: string
  maxCount?: number
  multiple?: boolean
  beforeUpload?: (file: File) => boolean
  fileList?: UploadFile[]
  onChange?: (info: { fileList: UploadFile[] }) => void
  onRemove?: (file: UploadFile) => void
  disabled?: boolean
  children?: React.ReactNode
}
function Dragger({
  className,
  accept,
  multiple = false,
  beforeUpload,
  fileList = [],
  onChange,
  onRemove,
  disabled,
  children,
  maxCount = 1,
}: Props) {
  const ref = useRef<HTMLInputElement>(null)
  const id = useId()
  const receive = (files: FileList | null) => {
    if (disabled || !files) return
    const accepted = Array.from(files)
      .slice(0, maxCount)
      .filter((file) => beforeUpload?.(file) !== false)
    onChange?.({
      fileList: accepted.map((file) => ({
        uid: randomUUID(),
        name: file.name,
        size: file.size,
        originFileObj: file,
      })),
    })
  }
  return (
    <div className={cn('ui-upload-wrapper', className)}>
      <label
        htmlFor={id}
        className={cn('ui-upload-drag', disabled && 'ui-upload-disabled')}
        onDragOver={(event) => event.preventDefault()}
        onDrop={(event) => {
          event.preventDefault()
          receive(event.dataTransfer.files)
        }}
        aria-disabled={disabled}
      >
        {children || <UploadCloud />}
        <input
          ref={ref}
          id={id}
          className="sr-only"
          type="file"
          accept={accept}
          multiple={multiple}
          disabled={disabled}
          onChange={(event) => {
            receive(event.target.files)
            event.target.value = ''
          }}
        />
      </label>
      {fileList.map((file) => (
        <div key={file.uid} className="ui-upload-file">
          <span>{file.name}</span>
          <button
            type="button"
            aria-label="移除文件"
            disabled={disabled}
            onClick={() => {
              onRemove?.(file)
              onChange?.({
                fileList: fileList.filter((item) => item.uid !== file.uid),
              })
            }}
          >
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  )
}
export const Upload = { Dragger }
