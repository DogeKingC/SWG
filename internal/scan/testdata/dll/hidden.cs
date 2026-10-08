public class Hidden {
  public static object Go() {
    var t = System.Type.GetType("System.IO.File, System.IO.FileSystem");
    return t.GetMethod("ReadAllBytes").Invoke(null, new object[] { "secret.txt" });
  }
}
