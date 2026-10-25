$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$request = [Console]::In.ReadToEnd() | ConvertFrom-Json
Add-Type -AssemblyName System.Windows.Forms
# Windows Common Item Dialog in file-picking mode (Explorer-style address bar,
# search and navigation); no legacy SHBrowseForFolder / FolderBrowserDialog.
# https://learn.microsoft.com/windows/win32/shell/common-file-dialog
Add-Type -ReferencedAssemblies System.Windows.Forms, System.Drawing -TypeDefinition @'
using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Windows.Forms;

public static class SWDFolderDialog {
  [ComImport, Guid("DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7")]
  class FileOpenDialog {}

  [ComImport, Guid("42F85136-DB7E-439C-85F1-E4075D135FC8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  interface IFileDialog {
    [PreserveSig] int Show(IntPtr owner);
    void SetFileTypes(uint count, IntPtr types);
    void SetFileTypeIndex(uint index);
    void GetFileTypeIndex(out uint index);
    void Advise(IntPtr events, out uint cookie);
    void Unadvise(uint cookie);
    void SetOptions(uint options);
    void GetOptions(out uint options);
    void SetDefaultFolder(IShellItem folder);
    void SetFolder(IShellItem folder);
    void GetFolder(out IShellItem folder);
    void GetCurrentSelection(out IShellItem item);
    void SetFileName([MarshalAs(UnmanagedType.LPWStr)] string name);
    void GetFileName(out IntPtr name);
    void SetTitle([MarshalAs(UnmanagedType.LPWStr)] string title);
    void SetOkButtonLabel([MarshalAs(UnmanagedType.LPWStr)] string text);
    void SetFileNameLabel([MarshalAs(UnmanagedType.LPWStr)] string text);
    void GetResult(out IShellItem result);
    void AddPlace(IShellItem item, uint placement);
    void SetDefaultExtension([MarshalAs(UnmanagedType.LPWStr)] string extension);
    void Close(int result);
    void SetClientGuid(ref Guid guid);
    void ClearClientData();
    void SetFilter(IntPtr filter);
  }

  [ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
  interface IShellItem {
    void BindToHandler(IntPtr context, ref Guid handler, ref Guid iid, out IntPtr result);
    void GetParent(out IShellItem parent);
    void GetDisplayName(uint kind, out IntPtr name);
    void GetAttributes(uint mask, out uint attributes);
    void Compare(IShellItem other, uint hint, out int order);
  }

  [DllImport("shell32.dll", CharSet=CharSet.Unicode, PreserveSig=false)]
  static extern void SHCreateItemFromParsingName(string path, IntPtr context, ref Guid iid, out IShellItem item);

  [DllImport("user32.dll")]
  static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")]
  static extern bool IsWindow(IntPtr window);
  [DllImport("user32.dll")]
  static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);

  sealed class DpiScope : IDisposable {
    readonly IntPtr previous;
    public DpiScope() {
      // Like dance-trail's picker: opt in before creating any HWND or dialog.
      // PMv2 scales native dialog controls as well as the window frame.
      try { previous = SetThreadDpiAwarenessContext(new IntPtr(-4)); }
      catch (EntryPointNotFoundException) { } // Older Windows keeps its default.
    }
    public void Dispose() {
      if (previous != IntPtr.Zero) SetThreadDpiAwarenessContext(previous);
    }
  }

  sealed class WindowHandle : IWin32Window {
    readonly IntPtr handle;
    public WindowHandle(IntPtr value) { handle = value; }
    public IntPtr Handle { get { return handle; } }
  }

  sealed class DialogOwner : Form {
    protected override bool ShowWithoutActivation { get { return true; } }
    public DialogOwner(IntPtr foreground) {
      ShowInTaskbar = false;
      FormBorderStyle = FormBorderStyle.None;
      Opacity = 0;
      Width = Height = 1;
      StartPosition = FormStartPosition.Manual;
      var area = Screen.FromHandle(foreground).WorkingArea;
      Left = area.Left + area.Width / 2;
      Top = area.Top + area.Height / 2;
    }
  }

  public static string Select(string title, string initial) {
    // Capture the browser before the helper creates its own windows.
    IntPtr foreground = GetForegroundWindow();
    using (var dpi = new DpiScope())
    using (var owner = new DialogOwner(foreground)) {
      // Consume SW_HIDE from the hidden helper's first Show without activating
      // a dummy window. Ownership keeps the picker above the foreground browser.
      // Use a local proxy so timeout/process cancellation cannot leave a foreign
      // browser window disabled by the modal dialog.
      if (foreground != IntPtr.Zero && IsWindow(foreground))
        owner.Show(new WindowHandle(foreground));
      else owner.Show();
      owner.Hide();
      return SelectOwned(title, initial, owner.Handle);
    }
  }

  static string SelectOwned(string title, string initial, IntPtr owner) {
    var dialog = (IFileDialog)new FileOpenDialog();
    try {
      uint options; dialog.GetOptions(out options);
      // FILEMUSTEXIST plus filesystem/path/no-recent options.
      dialog.SetOptions(options | 0x1000u | 0x40u | 0x800u | 0x8u | 0x02000000u);
      dialog.SetTitle(title);
      if (File.Exists(initial)) {
        dialog.SetFileName(Path.GetFileName(initial));
        initial = Path.GetDirectoryName(initial);
      }
      if (Directory.Exists(initial)) {
        var iid = typeof(IShellItem).GUID;
        IShellItem folder; SHCreateItemFromParsingName(initial, IntPtr.Zero, ref iid, out folder);
        try {dialog.SetFolder(folder);} finally {Marshal.ReleaseComObject(folder);}
      }
      int hr = dialog.Show(owner);
      if (hr == unchecked((int)0x800704C7)) return "";
      Marshal.ThrowExceptionForHR(hr);
      IShellItem result; dialog.GetResult(out result);
      try {
        IntPtr path; result.GetDisplayName(0x80058000u, out path); // SIGDN_FILESYSPATH
        try {return Marshal.PtrToStringUni(path);} finally {Marshal.FreeCoTaskMem(path);}
      } finally {Marshal.ReleaseComObject(result);}
    } finally {Marshal.ReleaseComObject(dialog);}
  }
}
'@
$selected = [SWDFolderDialog]::Select($request.title, $request.initial)
[Console]::Out.Write((@{path=$selected} | ConvertTo-Json -Compress))
