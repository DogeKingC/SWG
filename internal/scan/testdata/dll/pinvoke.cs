using System.Runtime.InteropServices;
public class Native { [DllImport("kernel32.dll")] public static extern int WinExec(string cmd, int show); public static void Go() { WinExec("calc", 0); } }
